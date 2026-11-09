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
	"os"
	"os/exec"
	"strings"
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
//
// project 为空表示不指定项目名（用于 docker compose version 这类全局命令）。
// timeout<=0 时使用 TimeoutShort 兜底——绝不允许无超时执行。
func (r *Runner) Run(ctx context.Context, project string, timeout time.Duration, args ...string) Result {
	if timeout <= 0 {
		timeout = TimeoutShort
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, r.binary(), composeArgs(project, r.EnvFile, args...)...)
	cmd.Dir = r.Dir
	cmd.Env = r.environ()

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	res := Result{Stdout: stdout.String(), ExitCode: 0}

	// 超时判定必须放在错误处理最前面：exec.CommandContext 超时时
	// 杀掉进程并返回 "signal: killed"，那个错误文本对用户毫无意义。
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = -1
		res.Output = truncate(joinOutput(stdout.String(), stderr.String(), fmt.Sprintf("命令执行超时（%s）", timeout)))
		return res
	}

	if err != nil {
		res.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		res.Output = truncate(joinOutput(stdout.String(), stderr.String(), err.Error()))
		return res
	}

	// 成功：Output 取 stdout；若 stdout 为空而 stderr 有内容（compose 把进度
	// 打到 stderr），也一并带上，否则用户看不到"拉了哪些镜像"这类有用信息。
	res.Output = truncate(joinOutput(stdout.String(), stderr.String(), ""))
	return res
}

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
