package envelope

// payload_compose_pull.go：编排「更新镜像」的异步任务协议。
//
// ============================ 为什么必须异步 ============================
//
// compose pull 拉一个几百 MB 的镜像要几十秒到几分钟。如果让一次 RPC 从头等到尾：
//   - 会撞上 Hub 的请求超时，用户看到"失败"，实际几秒后镜像被换了；
//   - 用户看不到任何进度，只能盯着转圈，无法判断是卡住还是在拉大层。
//
// 因此拆成 start（立即返回 task_id）+ get（增量轮询）。
//
// ============================ 为什么输出是增量的 ============================
//
// 轮询会重复调用 get。若每次都返回全量输出，网络与前端渲染都随轮次线性增长
// （一次 pull 可能产生上千行）。用单调递增的 seq + after 游标：
//   - 正常轮询只拿新增的几行；
//   - 断线重连传 last seq 即可续上，不需要额外的会话状态。
//
// ============================ truncated 的含义 ============================
//
// Agent 侧缓冲有上限（防止一次 pull 把内存吃光）。超出后**丢弃最旧的**、
// 置 truncated=true，并保持 seq 单调递增——于是前端会看到 seq 跳号，
// 这就是"中间有内容被丢弃"的明确信号。
//
// 保留尾部而不是头部：pull 的失败原因（认证失败、镜像不存在）总在末尾。

// ComposePullStartPayload 启动一次编排镜像更新任务。
type ComposePullStartPayload struct {
	// Name 是 compose 项目名。
	Name string `json:"name"`
	// RequestID 是幂等键。
	//
	// 同一个 RequestID 重复 start 会返回**同一个任务**，不会重复执行命令
	// （Hub 重试是常态）。但同一个 RequestID 配不同 Name 属于调用方错误，
	// 必须拒绝——否则会静默返回另一个项目的任务，用户以为操作的是自己选的。
	RequestID string `json:"request_id"`
}

// ComposePullGetPayload 轮询任务进度。
type ComposePullGetPayload struct {
	TaskID string `json:"task_id"`
	// After 表示"只要 seq 大于它的输出"；0 表示从头取。
	After int64 `json:"after"`
}

// ComposePullOutput 是一行流式输出。
type ComposePullOutput struct {
	// Seq 单调递增，从 1 开始。
	Seq int64 `json:"seq"`
	// Stream 是 "stdout" 或 "stderr"。
	Stream string `json:"stream"`
	// Text 是**已脱敏**的输出行（不含行尾换行）。
	Text string `json:"text"`
}

// ComposePullResult 是 start 与 get 共用的应答。
//
// 两者共用一套结构：前端只需实现一次解析，start 就是"游标为 0 的首个快照"。
type ComposePullResult struct {
	TaskID string `json:"task_id"`
	Name   string `json:"name"`
	// WorkingDir 是命令**实际的工作目录**（compose 在此目录下查找 compose.yml）。
	//
	// 单独给出而不是拼进 Command：拼成 "cd X && ..." 会让人以为
	// Agent 通过 shell 执行，而实际始终是 exec(argv) + 进程工作目录。
	WorkingDir string `json:"working_dir,omitempty"`

	// Command 是**真实执行的命令**展示形式：可执行文件绝对路径 + 实际参数。
	//
	// 只做展示用的 shell 引号包裹（便于用户看清参数边界），
	// 但 Agent **绝不通过 shell 执行它**——执行始终是 exec(argv)，
	// 因此引号不会带来注入面。
	//
	// 刻意显示真实的绝对 --env-file 路径：编造一个"好看的相对路径"
	// 会让用户复制命令去手工排查时失败，那比看起来丑糟糕得多。
	// 路径不是密码，展示它是安全的。
	Command string `json:"command"`

	// Status 是 queued / running / succeeded / failed。
	Status string `json:"status"`

	// Output 是本次请求新增的行（seq > after）。
	Output []ComposePullOutput `json:"output"`
	// NextCursor 是下次轮询应传的 After。
	NextCursor int64 `json:"next_cursor"`
	// Truncated 表示中间有输出因缓冲上限被丢弃（seq 会跳号）。
	Truncated bool `json:"truncated"`

	// ExitCode 仅终态有值；成功时为 0。
	//
	// 用指针是为了区分"还没结束"（null）与"结束且退出码 0"。
	// 用 0 表示未知会让前端把"还在跑"显示成"成功"。
	ExitCode *int `json:"exit_code,omitempty"`
	// Error 是失败原因（固定中文文案，不含 .env 内容与凭证明文）。
	Error string `json:"error,omitempty"`

	StartedAt  string `json:"started_at,omitempty"`
	FinishedAt string `json:"finished_at,omitempty"`
}
