package runtime

// container_exec.go：在容器内一次性执行命令（container.exec 的运行时实现）。
//
// ============================ 为什么是"一次性" ============================
//
// 见 protocol/envelope/payload_container_exec.go 的说明：控制通道是信令通道，
// 不适合搬 stdin/stdout 逐键流。这里刻意**不**AttachStdin，也不开 Tty——
// 一旦开 Tty，引擎输出就不再是多路复用帧，而是原样字节流，
// 我们也就无法把 stdout 与 stderr 分开；而"报错在哪一路"恰恰是排障要的信息。
//
// ============================ 输出上限与超时的诚实性 ============================
//
// 命令输出无界（`yes`、`cat /dev/urandom`）。这里读满上限就停止读取并置
// Truncated，绝不无界累积。超时后有两条必须承认的事实：
//  1. 我们只是**断开输出**，没有能力保证容器内那个进程被杀掉；
//  2. 此时退出码没有意义，用 TimedOut 标记，而不是编一个 0 或 1。
//
// 这两点写进应答字段而不是靠文档，是因为调用方（界面或 AI）看不到文档。

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ContainerExecRuntime 是可选能力：在容器内执行一条命令。
//
// 与 InspectContainer 一样用可选接口，不改既有 Runtime 接口——
// 已有 mock 不必跟着改动，旧引擎也只是没有这个方法而已。
type ContainerExecRuntime interface {
	ExecContainer(ctx context.Context, idOrName string, opts ExecOptions) (ExecResult, error)
}

// exec 输出的硬上限：单路 256 KiB。
//
// 取值理由：控制台里一次命令的输出，人真正会看的是前后几屏；
// 256 KiB 足以覆盖 `docker logs` 级别的排障输出，又远小于单条消息 8 MiB 的上限，
// 不会把 WebSocket 出站队列（32 条）顶满。
const (
	maxExecOutputBytes = 256 << 10
	// maxExecReadBytes 是实际读取的总量：两路各一上限，再加帧头余量。
	maxExecReadBytes = maxExecOutputBytes*2 + 64<<10

	// exec 超时的默认值与夹取区间（秒）。
	// 上限 120 秒：再长的命令应该放进后台任务，占用一条同步请求只会让界面卡死。
	defaultExecTimeoutSec = 30
	maxExecTimeoutSec     = 120
)

// ExecOptions 是一次命令执行的参数。
type ExecOptions struct {
	// Command 是命令向量，不经过 shell 解析。
	Command []string
	// User 为空表示镜像默认用户；不默认 root（很多镜像刻意用非 root）。
	User string
	// WorkDir 为空表示镜像配置的 WorkingDir。
	WorkDir string
	// Timeout 是墙钟上限；<=0 时用默认值，超过上限时夹到上限。
	Timeout time.Duration
}

// ExecResult 是执行结果（已截断）。
type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	// Truncated 表示输出被上限截断。
	Truncated bool
	// TimedOut 表示等待超时；此时 ExitCode 为 -1（无法确定）。
	TimedOut bool
	// Duration 是实际耗时。
	Duration time.Duration
}

// execCreateReq 是 POST /containers/{id}/exec 的请求体。
//
// 字段名是 Docker 的 PascalCase，不能改成 json 风格：
// 引擎按原名解析，改名会被静默忽略（表现为"命令跑了但没有输出"）。
type execCreateReq struct {
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Tty          bool     `json:"Tty"`
	Cmd          []string `json:"Cmd"`
	User         string   `json:"User,omitempty"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
}

// execCreateResp 是创建 exec 会话的应答。
type execCreateResp struct {
	ID string `json:"Id"`
}

// execStartReq 是 POST /exec/{id}/start 的请求体。
//
// Detach=false 是核心：true 会立刻返回 200 而把进程甩到后台，
// 我们就再也拿不到输出，退出码也无从判断。
type execStartReq struct {
	Detach bool `json:"Detach"`
	Tty    bool `json:"Tty"`
}

// execInspectResp 是 GET /exec/{id}/json 的应答（只取两个字段）。
type execInspectResp struct {
	Running  bool `json:"Running"`
	ExitCode int  `json:"ExitCode"`
}

// ExecContainer 在容器内执行一条命令并返回输出快照。
func (e *engine) ExecContainer(ctx context.Context, idOrName string, opts ExecOptions) (ExecResult, error) {
	if err := validateRef(idOrName); err != nil {
		return ExecResult{}, err
	}
	if len(opts.Command) == 0 {
		return ExecResult{}, errors.New("命令不能为空")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultExecTimeoutSec * time.Second
	}
	if timeout > maxExecTimeoutSec*time.Second {
		timeout = maxExecTimeoutSec * time.Second
	}
	started := time.Now()

	// ---- 1. 创建 exec 会话 ----
	created, err := e.client.do(ctx, http.MethodPost, containerPath(idOrName)+actionExec,
		mustJSONReader(execCreateReq{
			AttachStdin:  false, // 一次性执行：不接收输入
			AttachStdout: true,
			AttachStderr: true,
			Tty:          false, // 必须 false：Tty 模式没有帧头，无法分离 stdout/stderr
			Cmd:          opts.Command,
			User:         opts.User,
			WorkingDir:   opts.WorkDir,
		}))
	if err != nil {
		return ExecResult{}, e.translateContainerOp(err, "执行命令")
	}
	var session execCreateResp
	if err := json.Unmarshal(created, &session); err != nil {
		return ExecResult{}, fmt.Errorf("解析 exec 会话失败: %w", err)
	}
	if session.ID == "" {
		return ExecResult{}, errors.New("引擎未返回 exec 会话 ID")
	}

	// ---- 2. 启动并读取输出流 ----
	// 超时用独立 ctx：到达上限时 net/http 会关闭响应体，读取随即返回错误。
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	stream, err := e.client.postStreamJSON(runCtx, pathExecPrefix+session.ID+"/start",
		execStartReq{Detach: false, Tty: false})
	if err != nil {
		return ExecResult{}, e.translateContainerOp(err, "执行命令")
	}
	defer func() { _ = stream.Close() }()

	raw, readErr := io.ReadAll(io.LimitReader(stream, maxExecReadBytes))
	stdout, stderr, truncated := splitExecFrames(raw)
	// 读满上限说明后面还有，只是我们主动停了。
	if len(raw) >= maxExecReadBytes {
		truncated = true
	}
	timedOut := errors.Is(readErr, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded)
	// 本地取消（客户端断开）与 deadline 都要如实回答，不要伪装成"命令执行成功"。
	if readErr != nil && !timedOut && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, context.Canceled) {
		return ExecResult{}, fmt.Errorf("读取命令输出失败: %w", readErr)
	}

	res := ExecResult{
		Stdout:    stdout,
		Stderr:    stderr,
		Truncated: truncated,
		TimedOut:  timedOut,
		ExitCode:  -1,
		Duration:  time.Since(started),
	}
	if timedOut {
		// 超时后不再查询退出码：那个会话的进程可能还在跑，读到的值不可信。
		return res, nil
	}

	// ---- 3. 查询退出码 ----
	// 用父 ctx 而不是已超时的 runCtx：输出流已经正常结束，这一步不该被超时带走。
	detail, err := e.client.do(ctx, http.MethodGet, pathExecPrefix+session.ID+"/json", nil)
	if err != nil {
		// 退出码查不到不影响"命令已经跑完"这个事实，如实返回 -1 而不是猜一个值。
		return res, nil
	}
	var inspected execInspectResp
	if err := json.Unmarshal(detail, &inspected); err != nil {
		return res, nil
	}
	if inspected.Running {
		// 流结束但会话仍在运行：引擎侧行为异常（或输出被我们读完截断后进程还在写）。
		res.TimedOut = true
		return res, nil
	}
	res.ExitCode = inspected.ExitCode
	return res, nil
}

// mustJSONReader 把请求体序列化进内存 Reader。
//
// 只在请求体是几十字节的小结构时使用（exec 创建请求），
// 因此不需要担心内存；不要把归档类大对象走到这里。
func mustJSONReader(v any) io.Reader {
	raw, err := json.Marshal(v)
	if err != nil {
		// 入参全是字符串/布尔，序列化不可能失败；真要失败也是编程错误。
		panic(fmt.Sprintf("序列化 exec 请求失败: %v", err))
	}
	return bytes.NewReader(raw)
}

// splitExecFrames 把 Docker 的多路复用流拆成 stdout / stderr。
//
// 帧格式：1 字节 stream 类型 + 3 字节保留 + 4 字节大端长度 + 正文。
// 与 logs 的解析不同，这里保留两路分离（日志面板只需要合并文本）。
//
// 兼容性：某些兼容实现（或代理层）会直接返回纯文本。识别方式与 logs 一致：
// 首帧不合法就整段当 stdout，宁可少分离也不要吞掉输出。
func splitExecFrames(raw []byte) (stdout, stderr string, truncated bool) {
	if len(raw) == 0 {
		return "", "", false
	}
	if !looksLikeExecFrames(raw) {
		text := string(raw)
		if len(text) > maxExecOutputBytes {
			// 按字节截断可能切坏多字节字符；用字符串前缀回退到 rune 边界。
			text = truncateUTF8(text, maxExecOutputBytes)
			truncated = true
		}
		return text, "", truncated
	}

	outBuf := make([]byte, 0, len(raw))
	errBuf := make([]byte, 0, 1024)
	for off := 0; off+logFrameHeaderLen <= len(raw); {
		stream := raw[off]
		size := int(binary.BigEndian.Uint32(raw[off+4 : off+8]))
		start := off + logFrameHeaderLen
		if size < 0 || start+size > len(raw) {
			// 尾部残缺：把剩余内容按 stdout 保留，不丢信息。
			outBuf = appendCapped(outBuf, raw[off:], maxExecOutputBytes)
			break
		}
		payload := raw[start : start+size]
		if stream == logStreamStdErr {
			errBuf = appendCapped(errBuf, payload, maxExecOutputBytes)
		} else {
			// stdout / stdin / 未知类型一律归到 stdout：
			// 我们没开 stdin，出现 0 只可能是引擎实现差异，丢掉更糟。
			outBuf = appendCapped(outBuf, payload, maxExecOutputBytes)
		}
		off = start + size
	}
	if len(outBuf) >= maxExecOutputBytes || len(errBuf) >= maxExecOutputBytes {
		truncated = true
	}
	return truncateUTF8(string(outBuf), maxExecOutputBytes), truncateUTF8(string(errBuf), maxExecOutputBytes), truncated
}

// looksLikeExecFrames 判断响应是否是合法的多路复用帧序列。
func looksLikeExecFrames(raw []byte) bool {
	if len(raw) < logFrameHeaderLen {
		return false
	}
	off := 0
	frames := 0
	for off+logFrameHeaderLen <= len(raw) {
		stream := raw[off]
		if stream > logStreamStdErr {
			return false
		}
		size := int(binary.BigEndian.Uint32(raw[off+4 : off+8]))
		if size < 0 || size > 32<<20 {
			return false
		}
		start := off + logFrameHeaderLen
		if start+size > len(raw) {
			return false
		}
		off = start + size
		frames++
	}
	return frames > 0 && off >= len(raw)-1
}

// appendCapped 追加但不超过上限，返回新切片（不保证是否截断，截断由调用方比对长度）。
func appendCapped(dst, src []byte, limit int) []byte {
	if len(dst) >= limit {
		return dst
	}
	if remain := limit - len(dst); len(src) > remain {
		src = src[:remain]
	}
	return append(dst, src...)
}

// truncateUTF8 按字节上限截断，并退回到合法的 UTF-8 边界。
//
// 为什么要退：截断点落在多字节字符中间时，返回的字符串会含替换字符，
// 前端显示成一串 �。宁可少一两个字符。
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	// 最多回退 3 字节（UTF-8 单字符最长 4 字节）。
	for i := 0; i < 3 && cut > 0; i++ {
		if (s[cut] & 0xC0) != 0x80 { // 不是续字节，说明切在字符边界
			break
		}
		cut--
	}
	return s[:cut]
}
