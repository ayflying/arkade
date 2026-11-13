// Package envelope：编排操作的异步任务协议。
//
// ============================ 为什么是异步 ============================
//
// 启动/重建/备份可能要几分钟。同步 RPC 会被 HTTP 超时切断，
// 而用户在切断之后既看不到进度、也不知道操作是否还在跑。
// 因此这里采用"start 立即返回 + get 增量轮询"的作业模型，
// 与原有的 compose.pull.start/get 完全同构。
//
// ============================ 输出是增量的 ============================
//
// Output 只返回 "seq > after" 的事件，客户端保存 next_cursor 再取下一批。
// 一次只回全量会让长任务（备份几 GB）的响应体无限增长。
package envelope

// 编排操作方法名。
const (
	// MethodComposeOperationStart 启动一次编排动作，立即返回任务标识。
	MethodComposeOperationStart = "compose.operation.start"
	// MethodComposeOperationGet 增量读取任务输出。
	MethodComposeOperationGet = "compose.operation.get"
)

// 动作取值。
const (
	ActionUp      = "up"
	ActionDown    = "down"
	ActionStop    = "stop"
	ActionRestart = "restart"
	ActionPull    = "pull"
	ActionRebuild = "rebuild"
	ActionRemove  = "remove"
	ActionBackup  = "backup"
)

// 任务状态取值。
const (
	OpStatusQueued    = "queued"
	OpStatusRunning   = "running"
	OpStatusSucceeded = "succeeded"
	OpStatusFailed    = "failed"
)

// 输出流取值。
//
// ============================ stream 的含义 ============================
//
//	"command" —— 一条**真实执行**的命令（argv 已按 shell 规则引用）。
//	"stdout"/"stderr" —— 该命令的输出，内容已脱敏。
//	"stage" —— 一个没有单一 argv 可如实展示的阶段（如"复制编排文件"）。
//
// ============================ 前端注意 ============================
//
// "command" 事件是**可执行命令**，可以高亮/复制。
// "stage" 事件不是命令，**不要**渲染成可复制执行的样式——
// 它没有对应的 argv，复制去跑只会得到"命令不存在"。
// 进度不要自行编造百分比：多命令的真实进度只能靠事件序列表达。
const (
	StreamCommand = "command"
	StreamStdout  = "stdout"
	StreamStderr  = "stderr"
	StreamStage   = "stage"
)

// ComposeOperationStartPayload 是启动编排动作的请求体。
type ComposeOperationStartPayload struct {
	// Name 是编排项目名。
	Name string `json:"name"`
	// RequestID 是幂等键。
	//
	//   - 同一 request_id + 同一组参数 → 返回同一个任务（命令只执行一次）；
	//   - 同一 request_id + 不同参数 → 返回 409，因为参数变了说明
	//     用户实际提交的是另一次操作，静默复用旧任务会让用户以为
	//     新参数已生效。
	//
	// 长度限制 8..128 字节（Agent 侧校验）。
	RequestID string `json:"request_id"`

	// Action 是动作名：up/down/stop/restart/pull/rebuild/remove/backup。
	Action string `json:"action"`

	// Config 与 Env 仅 up 使用。
	//
	// 这里**不**复用 ComposeSpecPayload，而是直接展开字段：
	// up 是唯一需要完整 spec 的动作，其余动作传 spec 属于参数错误。
	Config     string `json:"config,omitempty"`
	Env        string `json:"env,omitempty"`
	ExpectHash string `json:"expect_hash,omitempty"`

	// Services 仅 stop/restart 使用。
	//
	// 顺序不影响语义（Agent 侧会排序去重后参与指纹计算），
	// 因此同样的服务集合不会因为顺序不同而被判为冲突。
	Services []string `json:"services,omitempty"`

	// RemoveVolumes 用于 down 与 remove，表示是否连带删除数据卷。
	//
	// **默认 false**：删卷不可逆，绝不能因为点了"删除编排"就默认丢数据。
	RemoveVolumes bool `json:"remove_volumes,omitempty"`

	// IncludeVolumes 仅 backup 使用。
	IncludeVolumes bool `json:"include_volumes,omitempty"`
}

// ComposeOperationGetPayload 是读取任务的请求体。
type ComposeOperationGetPayload struct {
	// TaskID 是 start 返回的任务标识。
	TaskID string `json:"task_id"`
	// After 是已消费到的序号；返回 > After 的事件。首次传 0。
	After int64 `json:"after"`
}

// ComposeOperationEvent 是一条任务输出事件。
type ComposeOperationEvent struct {
	// Seq 是单调递增序号（从 1 开始）。
	//
	// **跳号即意味着中间有事件因缓冲区上限被丢弃**，
	// 客户端应据此提示"输出不完整"而不是静默拼接。
	Seq int64 `json:"seq"`
	// Stream 取值见 StreamCommand/StreamStdout/StreamStderr/StreamStage。
	Stream string `json:"stream"`
	// Text 是事件内容。
	//
	// stream=command 时是**已引用的 argv**；
	// stdout/stderr 时是**已脱敏**的一行输出。
	Text string `json:"text"`
}

// ComposeOperationResult 是任务快照。
type ComposeOperationResult struct {
	TaskID string `json:"task_id"`
	Name   string `json:"name"`
	// Action 回显本次动作，便于前端区分同一项目的多个任务。
	Action string `json:"action"`

	// WorkingDir 是命令的实际执行目录。
	//
	// 单独给出而不是拼进 Command：Command 里若带 "cd X && ..."，
	// 会让人以为中间经过 shell，实际上这里是 exec argv，没有 shell 参与。
	WorkingDir string `json:"working_dir,omitempty"`

	// Command 是**最近一次真实执行**的命令。
	//
	// queued 阶段为空：此时还没有任何命令被执行，展示一条"预计"命令
	// 会在参数变化后与实际执行的不一致。
	Command string `json:"command,omitempty"`

	// Status 取值见 OpStatus*。
	Status string `json:"status"`

	// Output 是增量事件（seq > 请求的 after）。
	Output []ComposeOperationEvent `json:"output,omitempty"`
	// NextCursor 是下次请求应传的 after。
	NextCursor int64 `json:"next_cursor"`
	// Truncated 表示缓冲区曾丢弃早期事件（seq 有跳号）。
	Truncated bool `json:"truncated,omitempty"`

	// ExitCode 是**失败那一步**的退出码。
	//
	// 用指针区分"未执行/进行中"（null）与"执行成功"（0）：
	// 零值 0 会被前端读成成功，而排队中的任务根本没跑过命令。
	ExitCode *int `json:"exit_code,omitempty"`

	// Error 是失败原因。
	//
	// **成功时不填**——否则前端会看到 status=succeeded 却带着 error
	// 而误判为失败。这里也绝不回显任何配置原文。
	Error string `json:"error,omitempty"`

	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`

	// BackupPath 是 backup 成功后的备份目录（相对工作区路径）。
	//
	// 只回相对路径：绝对路径会暴露 Agent 的磁盘布局，
	// 而用户在这个界面里需要的是"备份放在哪儿"这一信息本身。
	BackupPath string `json:"backup_path,omitempty"`
}
