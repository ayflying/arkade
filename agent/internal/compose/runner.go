package compose

// runner.go 封装 docker compose 命令行的调用。
//
// 三个关键点（都是踩过或差点踩坑的地方）：
//
//  1. **所有命令必须有超时**。compose pull 拉一个几百 MB 的镜像可能要好几分钟，
//     up 还要等健康检查；如果不设超时，一个卡住的命令会把 Agent 的处理器永久占住。
//     但超时也不能设短：设成 30 秒会把正常的大镜像拉取误杀，用户会以为拉取失败。
//     因此按动作分档：拉取/启动类给 10 分钟，探测/日志类给 30 秒。
//
//  2. **必须显式传 DOCKER_HOST 给子进程**。Agent 主程序是通过自己的配置发现
//     docker.sock 的，而 compose 子进程是独立进程、自己读环境变量——
//     这两条路径完全独立，是整套方案里最容易断的一环。只要 Agent 配的不是
//     默认套接字，子进程就必须被显式告知，否则它连的是编译器默认的
//     /var/run/docker.sock，表现是"Agent 明明能连上，compose 却连不上"。
//
//  3. **输出要截断**。失败时 compose 可能打出几百行构建日志，
//     原样塞进 JSON 会拖慢界面甚至撑爆 WebSocket 帧。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// 命令超时档位。
const (
	// TimeoutLong 用于可能很慢的动作：pull / up / rebuild / down。
	// 10 分钟是经验值：拉取大镜像加健康检查等待通常远小于它，
	// 又能兜住"镜像仓库无响应"这类挂死场景。
	TimeoutLong = 10 * time.Minute
	// TimeoutShort 用于轻量查询：ps / logs / version。
	TimeoutShort = 30 * time.Second
)

// maxOutputRunes 是回传命令输出的上限（字符数）。
// 保留尾部而不是头部：失败原因（端口冲突、镜像拉取失败、配置错误）总在末尾。
const maxOutputRunes = 4000

// Runner 执行 docker compose 命令。
type Runner struct {
	// DockerHost 是要传给子进程的 DOCKER_HOST。空表示沿用当前进程环境
	// （即不额外设置，让 compose 用它的默认值）。
	//
	// 为什么要这个字段：Agent 与 compose 是两条独立的连接路径。
	// Agent 主程序可能通过配置连自定义套接字，而 compose 子进程只认环境变量，
	// 不显式传递就会出现"主程序正常、compose 连不上"的分裂。
	DockerHost string
	// Binary 是 docker 可执行文件路径，默认 "docker"。
	Binary string
	// Dir 是命令的工作目录（通常设为项目目录，让 compose 自然找到 compose.yml）。
	Dir string
	// Env 是额外环境变量，便于测试注入。
	Env []string

	// EnvFile 是项目的 .env 绝对路径；空表示该项目没有 .env。
	//
	// 显式传 --env-file 而不是依赖 compose 的"自动读当前目录 .env"：
	//   - 自动读取依赖 cwd，一旦 cwd 变了就读不到（本项目里 cwd 是项目目录，
	//     看起来没问题，但这类隐式依赖在重构时最容易断）；
	//   - 显式传路径让"哪个文件参与插值"是完全确定的，便于排查。
	EnvFile string

	// ProjectName 是这份 Runner 对应的编排项目名。
	//
	// 由 runnerFor 填入，便于"已经拿到 runner 的调用方"再去读同一项目的
	// .env 与正文（脱敏用），而不必重新解析一遍 runnerFor——
	// 重解析会引入"两次读到的 .env 不一致"的窗口。
	ProjectName string

	// EnvExcludeKeys 是必须从**继承环境**里剔除的变量名。
	//
	// 这是本项目最容易出错、后果最严重的一处，单独说明：
	//
	// docker compose 的插值优先级是 shell 环境 > .env 文件。
	// 也就是说，如果 Agent 进程的环境里有 FOO，而项目 .env 里也定义了 FOO，
	// 那么 ${FOO} 会取 **Agent 进程的 FOO**，.env 里的值被静默忽略。
	//
	// 真实危害：Agent 自己就带着 ARKADE_AGENT_TOKEN、ARKADE_DATA_DIR 等变量。
	// 若某份编排里写了 ${ARKADE_AGENT_TOKEN}（恶意或用户笔误），
	// 它就会被展开成 Hub 的接入密钥，然后写进容器配置、出现在 docker inspect、
	// 甚至被容器里的进程读走。这不是理论风险，是"只要写错一个变量名就泄漏"。
	//
	// 因此规则是：把项目 .env 里定义的所有键，都从子进程的继承环境里剔除，
	// 保证 .env 是真的生效；同时无条件剔除 ARKADE_* 前缀（见 environ）。
	EnvExcludeKeys []string
}

// envFileArgs 返回 --env-file 参数（没有 .env 时为空）。
//
// 位置要求：--env-file 是 `docker compose` 的**全局选项**，
// 必须放在子命令之前（compose --env-file x.env up），放在 up 之后会被当作
// 未知参数而报错。
func (r *Runner) envFileArgs() []string {
	if strings.TrimSpace(r.EnvFile) == "" {
		return nil
	}
	return []string{"--env-file", r.EnvFile}
}

// binary 返回要执行的 docker 路径。
func (r *Runner) binary() string {
	if r.Binary != "" {
		return r.Binary
	}
	return "docker"
}

// Result 是一次命令执行的结果。
type Result struct {
	// Output 是命令输出，**已截断**。成功时是 stdout，失败时是 stdout+stderr 合并。
	Output string
	// Stdout 是未截断的 stdout（供需要解析 JSON 的调用方使用）。
	Stdout string
	// ExitCode 是退出码；超时或启动失败时为 -1。
	ExitCode int
	// TimedOut 标记是否因超时被终止，便于上层给出"超时"而不是"失败"的提示。
	TimedOut bool
}

// ErrTimeout 表示命令超时。
var ErrTimeout = errors.New("compose 命令执行超时")

// composeArgs 拼出 "compose" 子命令及其全局参数。
//
// --project-name 显式指定而不是靠目录名推断：目录名虽然与项目名相同，
// 但显式指定能让命令在任何工作目录下行为一致，也避免 compose 对目录名
// 做额外的大小写/字符转换后与 Hub 记录的项目名对不上。
//
// envFile 是项目 .env 的绝对路径（空表示没有），作为全局选项放在最前面。
func composeArgs(project, envFile string, args ...string) []string {
	out := make([]string, 0, len(args)+6)
	out = append(out, "compose")
	if project != "" {
		out = append(out, "--project-name", project)
	}
	if strings.TrimSpace(envFile) != "" {
		out = append(out, "--env-file", envFile)
	}
	return append(out, args...)
}

// Run 执行一条 docker compose 命令。
// Run 执行一条 docker compose 命令（**原版实现，字节语义完全不变**）。
//
// project 为空表示不指定项目名（用于 docker compose version 这类全局命令）。
// timeout<=0 时使用 TimeoutShort 兜底——绝不允许无超时执行。
//
// ============================ 为什么不复用 RunStream ============================
//
// Run 的调用方会**解析 Stdout 的原始字节**：compose ps --format json 输出的是
// 一段 JSON，末尾没有换行，且可能长到几十 KB 单行。
// 若用"逐行回调 + 每行补 \n"的实现，会：
//   - 给无尾换行的输出强加一个 \n（字节不再相同）；
//   - 超长单行被 Scanner 的缓冲上限截断（JSON 直接被切碎）。
//
// 这两点都会让既有调用静默出错。
//
// 因此 Run 保持 bytes.Buffer 直连 cmd.Stdout/Stderr 的原版形态，
// 与 RunStream 只共享"参数/环境/超时"的构造逻辑（composeArgs / environ）。
func (r *Runner) Run(ctx context.Context, project string, timeout time.Duration, args ...string) Result {
	if timeout <= 0 {
		timeout = TimeoutShort
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	argv := composeArgs(project, r.EnvFile, args...)
	full := append([]string{r.binary()}, argv...)
	cmd := exec.CommandContext(runCtx, full[0], argv...)
	cmd.Dir = r.Dir
	cmd.Env = r.environ()

	// 与 RunStream 一致：超时时要杀整个进程组，否则 compose fork 出的
	// 子进程会占着管道，让 Wait 一直等下去。
	cmd.SysProcAttr = sysProcAttrSetPgid()
	cmd.Cancel = func() error {
		return killProcessGroup(cmd)
	}

	// 任务上下文里执行时，上报这条真实命令。
	ob := observerFrom(ctx)
	if ob != nil {
		ob.command(full, r)
	}

	// ============================ 实时性 vs 字节精确 ============================
	// 必须用 io.MultiWriter，让 cmd.Run **执行期间**就把输出推给观察者：
	// 只在命令结束后补发，up/rebuild 这类长操作会长时间空白，
	// 用户以为卡死了——这正是要解决的问题本身。
	//
	// stdout 写入顺序不变：MultiWriter 先写 &stdout（bytes.Buffer），
	// 再写观察者。因此 Result.Stdout 的精确字节与没有观察者时**完全一致**
	// （compose ps 的 JSON 解析依赖这一点）。
	var stdout, stderr bytes.Buffer
	if ob != nil {
		outW := newStreamWriter(ob.stdout)
		errW := newStreamWriter(ob.stderr)
		cmd.Stdout = io.MultiWriter(&stdout, outW)
		cmd.Stderr = io.MultiWriter(&stderr, errW)
		err := cmd.Run()

		// 补发两路各自残留的半行（os/exec 返回时已 copy 完毕）。
		outW.Close()
		errW.Close()
		return r.finish(runCtx, timeout, err, stdout.String(), stderr.String())
	}

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	return r.finish(runCtx, timeout, err, stdout.String(), stderr.String())
}

// finish 由 (ctx, timeout, err, stdout, stderr) 组装 Result。
//
// 抽出来是为了让"有观察者"与"无观察者"两条执行路径共用**同一套**
// 结论逻辑——否则两条路径对超时/退出码的处理稍有偏差，就会出现
// "在任务里跑和直接跑结果不一样"的诡异问题。
func (r *Runner) finish(runCtx context.Context, timeout time.Duration, err error, outStr, errStr string) Result {
	res := Result{Stdout: outStr, ExitCode: 0}

	// 超时判定必须放在错误处理最前面：exec.CommandContext 超时时
	// 杀掉进程并返回 "signal: killed"，那个错误文本对用户毫无意义。
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		res.Output = truncate(joinOutput(outStr, errStr, fmt.Sprintf("命令执行超时（%s）", timeout)))
		return res
	}

	if err != nil {
		res.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		res.Output = truncate(joinOutput(outStr, errStr, err.Error()))
		return res
	}

	// 成功：Output 取 stdout；若 stdout 为空而 stderr 有内容（compose 把进度
	// 打到 stderr），也一并带上，否则用户看不到"拉了哪些镜像"这类有用信息。
	res.Output = truncate(joinOutput(outStr, errStr, ""))
	return res
}

// StreamSink 接收子进程输出的一行（**不含行尾换行**）。
//
// 实现方必须保证并发安全：RunStream 会用同一把锁串行化 stdout/stderr。
type StreamSink interface {
	Stdout(line string)
	Stderr(line string)
}

// streamWriter 是给 cmd.Stdout / cmd.Stderr 用的行缓冲写入器。
//
// ============================ 为什么不用 StdoutPipe ============================
//
// 手写管道 + 自己起 goroutine 有两个真实陷阱：
//   - 必须在 Start **之前**起 goroutine，否则管道写满会阻塞子进程；
//   - Wait 会**关闭**管道，若此时读者还没读完，尾部落日志就丢了
//     （而 compose 的失败原因恰好在尾部）。
//
// 直接给 cmd.Stdout/Stderr 赋值则没有这些问题：os/exec 会在 Wait 内
// 同步完成 copy（内部只起 goroutine 当 Writer 不是 *os.File 时，
// 并且**保证**它们结束后才返回）。因此这里只需实现一个 Write。
//
// 行缓冲语义：按 \n 切分后回调；**残留的半行**在 Close 时补发，
// 因此不会因为进程输出不以换行结尾而丢掉最后一行。
// ============================ 超长行绝不 emit 前缀 ============================
//
// 直觉做法是"到上限就先发一段"。那是**泄密**：
// 一个 100KB 的行里，密码可能横跨 64KB 边界，
// 先发前半段就会把密码的前半个字符送出去，且破坏"按完整行脱敏"的前提
// （脱敏器只看得到半行，匹配不上完整值）。
//
// 因此超限时：**整行丢弃**，只发一个固定标记，并把 truncated 置位。
// 调用方据此知道"这里少了内容"，但拿不到任何原文片段。
type streamWriter struct {
	mu     sync.Mutex
	emit   func(string)
	buf    []byte
	closed bool
	// discarding 表示当前行已超限、正在丢弃剩余部分直到换行。
	discarding bool
}

// newStreamWriter 构造行缓冲写入器。
func newStreamWriter(emit func(string)) *streamWriter {
	return &streamWriter{emit: emit}
}

// Write 实现 io.Writer。
func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// ============================ io.Writer 契约 ============================
	// Write 必须报告"消费了多少**输入**字节"。本实现总是把入参全部收下
	// （丢弃也是消费），因此所有返回点都必须是入口的 len(p)。
	//
	// 若在丢弃态返回 0，os/exec 的 copy goroutine 会认为发生短路写，
	// 报 io.ErrShortWrite 并让整条命令报错——用户看到的"拉取失败"
	// 实际上是我们自己的 bug。
	nIn := len(p)

	// 正在丢弃超长行：整块跳过直到遇到换行，**只**把换行之后的内容当新数据。
	//
	// 不能只"找换行然后 append 中间部分"：换行之前的每个字节都属于那条
	// 被丢弃的行，append 回来它就变成了新行的开头——正是跨边界泄漏。
	if w.discarding {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			return nIn, nil // 整块都属于被丢弃的行
		}
		w.discarding = false
		p = p[i+1:] // 只保留换行之后的残余
		if len(p) == 0 {
			return nIn, nil
		}
	}

	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			// 还没有完整行。若已超限：整行丢弃（**不发任何前缀**）。
			if len(w.buf) > maxLineBytes {
				w.buf = w.buf[:0]
				w.discarding = true
				w.emit(longLineMarker)
			}
			return nIn, nil
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		// 单行超限（确实以换行结尾但太长）：同样整行丢弃。
		if len(line) > maxLineBytes {
			w.emit(longLineMarker)
			continue
		}
		w.emit(strings.TrimSuffix(line, "\r"))
	}
}

// Close 补发残留的半行。
func (w *streamWriter) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	if len(w.buf) > 0 {
		// 收尾时若残留半行已超限，同样整行丢弃而不是发出去。
		if len(w.buf) > maxLineBytes {
			w.emit(longLineMarker)
		} else {
			w.emit(strings.TrimSuffix(string(w.buf), "\r"))
		}
		w.buf = nil
	}
}

// RunStream 执行命令并把输出逐行推给 sink。
func (r *Runner) RunStream(ctx context.Context, project string, timeout time.Duration, sink StreamSink, args ...string) Result {
	if timeout <= 0 {
		timeout = TimeoutShort
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	argv := composeArgs(project, r.EnvFile, args...)
	full := append([]string{r.binary()}, argv...)
	cmd := exec.CommandContext(runCtx, full[0], argv...)
	cmd.Dir = r.Dir
	cmd.Env = r.environ()
	// ============================ 必须杀整个进程组 ============================
	// exec.CommandContext 默认只 kill 直接子进程。真实场景里 compose 会 fork
	// 出子进程（拉取、解包），它们继承同一批管道描述符；只杀父进程时，
	// 这些"孙进程"还活着并占着管道，cmd.Wait() 会一直等 copy 结束——
	// 表现为任务永远 running，直到子进程自己退出。
	//
	// 因此超时/取消时杀整个进程组。Setpgid 让子进程成为新进程组的组长。
	cmd.SysProcAttr = sysProcAttrSetPgid()
	cmd.Cancel = func() error {
		return killProcessGroup(cmd)
	}

	// 任务上下文里执行时，把输出同时旁路给观察者。
	//
	// 用函数组合而不是在回调里判断：sink 与 observer 是两条独立的
	// 消费路径，任一为 nil 都不影响另一条，也不会让"有没有观察者"
	// 改变 sink 的调用时序。
	ob := observerFrom(ctx)
	var out sinkFn = sink.Stdout
	var errFn sinkFn = sink.Stderr
	if ob != nil {
		ob.command(full, r)
		out = func(line string) { sink.Stdout(line); ob.stdout(line) }
		errFn = func(line string) { sink.Stderr(line); ob.stderr(line) }
	}

	// 两路输出共用一把锁：stdout 与 stderr 会由 os/exec 内部并发写入，
	// sink 的调用顺序必须与写入顺序一致，否则 seq 会错乱。
	var mu sync.Mutex
	outW := newStreamWriter(func(line string) {
		mu.Lock()
		out(line)
		mu.Unlock()
	})
	errW := newStreamWriter(func(line string) {
		mu.Lock()
		errFn(line)
		mu.Unlock()
	})
	cmd.Stdout = outW
	cmd.Stderr = errW

	err := cmd.Run()

	// cmd.Run 返回时 os/exec 已把两路输出全部 copy 完，
	// 此时补发各自的半行是安全的。
	outW.Close()
	errW.Close()

	res := Result{ExitCode: 0}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		res.Output = fmt.Sprintf("命令执行超时（%s）", timeout)
		return res
	}
	if err != nil {
		res.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		res.Output = err.Error()
		return res
	}
	return res
}

// 行缓冲参数。
const (
	// maxLineBytes 是单行上限。超过则**整行丢弃**（只发标记，不发前缀）。
	//
	// 定 64KB 的理由：compose 的正常输出行远小于它（进度行通常 < 200 字节），
	// 超长行基本只可能来自镜像内的异常输出。丢弃不影响可用性，
	// 却能同时守住内存上限与"敏感值跨块泄漏"两个边界。
	maxLineBytes = 64 << 10
	// longLineMarker 是超长行被丢弃时上报的占位行。
	longLineMarker = "（输出行过长，已截断丢弃）"
)

// agentEnvPrefix 是 Agent 自身配置变量名的前缀。
//
// 一律不传给 compose 子进程，理由见 Runner.EnvExcludeKeys 的注释：
// compose 的插值会优先取 shell 环境，Agent 的密钥因此可能被 ${} 展开进容器。
const agentEnvPrefix = "ARKADE_"

// environ 组装子进程环境变量。
//
// 三条规则，顺序有意义：
//  1. 先放入显式注入的 r.Env（测试用，优先级最高）；
//  2. 再从继承环境里复制，但剔除：ARKADE_* 前缀、项目 .env 已定义的键、
//     以及将被显式覆盖的 DOCKER_HOST；
//  3. 最后追加重设的 DOCKER_HOST。
//
// 显式覆盖而不是追加：环境变量重复时行为依赖具体实现，
// 先过滤掉继承来的 DOCKER_HOST 再加入我们的值，避免"到底哪个生效"的不确定性。
func (r *Runner) environ() []string {
	out := make([]string, 0, len(os.Environ())+len(r.Env)+1)
	if len(r.Env) > 0 {
		out = append(out, r.Env...)
	}

	exclude := make(map[string]struct{}, len(r.EnvExcludeKeys)+1)
	exclude["DOCKER_HOST"] = struct{}{}
	for _, k := range r.EnvExcludeKeys {
		k = strings.TrimSpace(k)
		if k != "" {
			exclude[k] = struct{}{}
		}
	}

	for _, kv := range os.Environ() {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if strings.HasPrefix(key, agentEnvPrefix) {
			continue
		}
		if _, skip := exclude[key]; skip {
			continue
		}
		out = append(out, kv)
	}

	if want := strings.TrimSpace(r.DockerHost); want != "" {
		out = append(out, "DOCKER_HOST="+want)
	}
	return out
}

// joinOutput 合并 stdout / stderr 与可选的错误尾巴，去掉空行噪声。
func joinOutput(stdout, stderr, tail string) string {
	parts := make([]string, 0, 3)
	if s := strings.TrimSpace(stdout); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(stderr); s != "" {
		parts = append(parts, s)
	}
	if t := strings.TrimSpace(tail); t != "" {
		parts = append(parts, t)
	}
	return strings.Join(parts, "\n")
}

// truncate 截断输出，保留尾部。
func truncate(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= maxOutputRunes {
		return s
	}
	return "…（输出过长，已截断前部）\n" + string(r[len(r)-maxOutputRunes:])
}
