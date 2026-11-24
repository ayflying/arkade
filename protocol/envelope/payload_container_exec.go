package envelope

// payload_container_exec.go：容器内一次性执行命令（container.exec）。
//
// ============================ 设计边界 ============================
//
// 这是**非交互**执行：一次请求 = 一个进程 = 一份输出快照。
// 不提供 stdin、不提供窗口尺寸、不保留 shell 状态（cd/export 不跨命令）。
// 需要多条语句时由调用方写成 `sh -c 'a && b'`。
//
// 之所以不做成"半交互"（保留 shell 进程、逐条灌命令）：
//   - 控制通道是信令通道，长连接槽位有限，长期占住会拖慢同服务器的其它操作；
//   - 保留 shell 需要 PTY 与窗口尺寸协商，那是另一套协议，混进来会让
//     这个方法的错误语义（超时/退出码/截断）全部变得不确定。
//
// ============================ 输出必须有上限 ============================
//
// 命令输出是无界数据（`cat /dev/urandom`、`yes`、编译日志）。
// Agent 侧强制截断并置 Truncated，绝不把无界内容塞进一条消息——
// 那会直接打爆 WebSocket 的读写队列（单条 8 MiB、队列 32 条）。

// ContainerExecPayload 是 container.exec 的请求。
type ContainerExecPayload struct {
	ContainerID string `json:"container_id"`

	// Command 是完整的命令向量，**不经过 shell 解析**。
	//
	// 例如用户要执行 `ls -l /tmp`，这里应是 ["ls","-l","/tmp"]；
	// 要交给 shell 解释管道或重定向，则写成 ["/bin/sh","-c","ps aux | head -5"]。
	//
	// 用向量而不是单个字符串：单个字符串必然要经过一层 shell 解析，
	// 那层解析会把用户输入里的引号、分号、变量展开全部激活，
	// 参数校验也就失去了意义（校验"一个字符串"不等于校验"真正执行的命令"）。
	Command []string `json:"command"`

	// User 是执行身份（用户名或 UID）。空表示容器镜像配置的默认用户。
	//
	// 不默认填 root：默认用户由镜像作者决定（很多镜像刻意用非 root），
	// 在这里硬编码 root 等于悄悄提权。
	User string `json:"user,omitempty"`

	// WorkDir 是工作目录。空表示镜像配置的 WorkingDir。
	WorkDir string `json:"workdir,omitempty"`

	// TimeoutSec 是本次执行的墙钟上限（秒）。Agent 侧会夹到安全区间，
	// 超时后断开输出并如实回答 TimedOut——**不承诺进程已被杀死**。
	TimeoutSec int `json:"timeout_sec,omitempty"`
}

// ContainerExecResult 是 container.exec 的应答。
type ContainerExecResult struct {
	// ExitCode 是命令退出码。TimedOut 为真时该值没有意义。
	ExitCode int `json:"exit_code"`

	// Stdout / Stderr 是**已截断**的输出文本。
	//
	// 分开而不是合并：排障时"报错在哪一路"本身就是信息
	// （例如镜像是把所有日志写 stderr 的）。
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`

	// Truncated 表示输出超过上限被截断（stdout 或 stderr 任一都算）。
	// 前端必须显式提示，否则用户会把"只看到开头"当成"命令只输出了这些"。
	Truncated bool `json:"truncated"`

	// TimedOut 表示等待超时，输出是超时前已收到的部分。
	TimedOut bool `json:"timed_out"`

	// DurationMs 是本次执行的实际耗时，用于判断"慢命令"。
	DurationMs int64 `json:"duration_ms"`
}
