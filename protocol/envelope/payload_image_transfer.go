package envelope

// payload_image_transfer.go 定义镜像 拉取/导出/导入 的载荷结构。
//
// ============================ 为什么字节不用 JSON 搬 ============================
//
// 控制连接是信令通道：单条入站消息上限 8 MiB、单条写超时 10 秒、
// 出站队列仅 32 条（见 link/session.go）。镜像归档是几百 MB 到几 GB，
// 直接塞进一个信封（base64 再膨胀 33%）必然先在几十 MB 处撞上限，
// 而失败现场表现为"连接被断开"，极易被误判成网络故障。
//
// 因此字节走**分块 RPC**（每块 ≤ 256 KiB）：
//   - Agent 永远不把整个归档读进内存；
//   - 每块天然可重试；
//   - 不新增入站端口，保持 Agent 反连 Hub 的网络形态。
//
// ============================ 任务的统一形状 ============================
//
// 所有镜像操作都异步化为一个 Task，pull/save/load.begin 与 status 返回**同一结构**，
// 前端不需要为每种操作写一套解析。字段语义固定：
//
//	TaskID      Agent 生成的 id，只有本进程认识（因此 release 无法越权）
//	Status      pending | running | succeeded | failed | canceled
//	Phase       粗粒度阶段（如 pulling / saving / receiving / loading / done）
//	Message     给用户看的中文说明（**绝不包含引擎原文**，见下）
//	Progress    0..1，*float64 —— 无法估算时为 null，不编造
//	BytesDone   已完成字节数
//	BytesTotal  总字节数，*int64 —— 未知时为 null（不能拿 0 冒充"未知"）
//	SHA256      归档校验和，可空（pull 不需要）
//	SizeBytes   归档大小，0 表示未知（pull 场景）
//
// 为什么 Progress 和 BytesTotal 用指针：0 与"未知"是两件事。
// 用一个 float64 字段并以 0 表示未知，会让界面显示"0%"——
// 用户看到"卡在 0%"和"正在下载但总大小未知"是完全不同的判断。
//
// ============================ 安全：绝不回显引擎原文 ============================
//
// Message 字段由 Agent 自己造句，**不转发 registry / 引擎的原始错误**：
// 那些文本常含 registry 认证失败详情、完整 URL 与内网地址。
// 需要细节时用户在本机命令里看得到，不必让它进 Hub 审计与浏览器。

// 任务状态取值。Hub 侧状态机必须只认这五个值，
// 新增状态要同时改这里与前端，避免"未知字符串"在界面上显示成空白。
const (
	// ImageTaskPending 已接受，尚未开始执行。
	ImageTaskPending = "pending"
	// ImageTaskRunning 正在执行。
	ImageTaskRunning = "running"
	// ImageTaskSucceeded 成功完成。
	ImageTaskSucceeded = "succeeded"
	// ImageTaskFailed 失败（Message 里有原因）。
	ImageTaskFailed = "failed"
	// ImageTaskCanceled 被取消。
	//
	// 注意：load 方向的取消**可能已经写入了部分镜像**。
	// 这一点无法回滚（镜像层一旦入库就没有事务），
	// 因此取消成功**不代表什么变更都没发生**，界面上必须如实说明。
	ImageTaskCanceled = "canceled"
)

// ImageTask 是所有镜像操作的统一任务视图。
type ImageTask struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
	// Operation 是产生该任务的操作（pull / save / load），
	// 让前端在列表里区分任务来源，而不必记住 id 的映射。
	Operation string `json:"operation"`
	// Reference 是该任务涉及的镜像引用（pull/save 有，load 之后才有值）。
	Reference string `json:"reference,omitempty"`
	Phase     string `json:"phase"`
	Message   string `json:"message"`
	// Progress 是 0..1 的完成比例；null 表示无法估算。
	Progress  *float64 `json:"progress"`
	BytesDone int64    `json:"bytes_done"`
	// BytesTotal 是总字节数；null 表示未知（不能拿 0 冒充）。
	BytesTotal *int64 `json:"bytes_total"`
	// SHA256 是归档校验和，可空。
	SHA256 string `json:"sha256,omitempty"`
	// SizeBytes 是归档大小，0 表示未知。
	SizeBytes int64 `json:"size_bytes"`
}

// ImagePullPayload 请求拉取一个公共 registry 镜像。
//
// ============================ 首批只支持公共 registry ============================
//
// 不收任何凭据字段（没有 username/password/token）。
// 理由：凭据一旦经 Hub 转手，就会同时出现在 Hub 的请求日志、
// 审计记录和浏览器网络面板里；而 Agent 完全可以在自己机器上配置凭据。
// 需要私有仓库时，正确的位置是 Agent 本地配置，不是这条协议。
//
// 也**不支持**任意 registry 地址：Reference 必须解析成合法的镜像引用
// （仓库名 + 可选标签/digest），不允许带协议前缀、不允许带 URL 用户名密码、
// 不允许控制字符。这不是"随意拒绝 .."，而是拒绝**不是镜像引用**的东西——
// 校验后仍会用 PathEscape/QueryEscape 正确转义，两道防线各司其职。
type ImagePullPayload struct {
	// Reference 形如 nginx:1.25、library/redis:7、ghcr.io/owner/app@sha256:...
	Reference string `json:"reference"`
	// Platform 可选，形如 linux/arm64；留空表示引擎默认平台。
	//
	// 首批**不通过 Web 暴露**这个字段（界面上没有入口），
	// 但协议保留并做严格校验：给了就必须是合法的 os/arch[/variant]，
	// 非法值直接拒绝而不是忽略——静默忽略会让调用方以为指定生效了。
	Platform string `json:"platform,omitempty"`
	// Digest 可选，若给出则拉取完成后必须与该 digest 一致，否则判定失败。
	//
	// 与 Platform 同样是"协议保留、首批不暴露 Web"。
	// 严格校验的理由相同：静默忽略一个用户以为生效的完整性约束，
	// 比直接报错危险得多。
	Digest string `json:"digest,omitempty"`
}

// ImageSavePayload 请求导出一个镜像为归档。
//
// Agent 会把归档流式写到专用 transfers 目录（0600），**不读进内存**。
// 成功后归档保留，直到显式 release 或超时过期（默认 30 分钟）：
// 保留是为了让 Hub 能分块拉取、断点续传。
type ImageSavePayload struct {
	// Reference 要导出的镜像引用或镜像 ID。
	Reference string `json:"reference"`
}

// ImageLoadBeginPayload 声明一次导入。
//
// 必须先声明大小：Agent 据此做**写入前**的磁盘配额预检，
// 并在 commit 阶段校验确切字节数。若让 Hub 边传边报大小，
// 就只能"传到一半才发现磁盘不够"，那时已经把配额用光了。
type ImageLoadBeginPayload struct {
	// SizeBytes 是即将上传的归档总字节数，**必须 > 0**。
	//
	// 为什么必须给出而不是"未知就边传边看"：没有确切大小就无法在写入前
	// 判断这次上传会不会撑爆配额。先占位再上传，最多是拒绝一次；
	// 而"传到 90% 才发现磁盘不够"会把已占用的配额白白浪费掉。
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 是归档的十六进制校验和，**允许为空**。
	//
	// 为什么允许空：SHA256 由 Hub 在接收字节流的同时增量计算，
	// 到 commit 时才拿到最终值。若强制 begin 时给出，接收端就必须
	// 先收完（或先落盘）再回答，等于把流式上传退化成本地中转。
	//
	// 但**不能两个地方都空**：那样就没有任何手段确认最终组装的字节
	// 与 Hub 那边是同一份，一次错位的重试会静默产生损坏的 tar。
	// 该规则由 commit 阶段强制——begin 有值则可沿用，begin 空则 commit 必填。
	SHA256 string `json:"sha256"`
}

// ImageChunkPutPayload 上传一块归档字节。
//
// ============================ offset 幂等语义（关键） ============================
//
// 只接受两种写入：
//  1. offset == 当前已收到字节数  → 正常追加；
//  2. offset < 当前已收到字节数，且**内容与已写入部分逐字节相同** → 视为重试，返回成功。
//
// 其它情况一律拒绝（尤其是 offset > 已收到）：那说明中间有块丢失，
// 继续写入只会得到一个错位的 tar。宁可让 Hub 重新传，也不能产生垃圾归档。
//
// 为什么要支持"同字节重试"：Hub 发出块后连接可能断在"Agent 已写入但
// 应答没送达"的时刻。Hub 重发同一块时，若直接报错，这个任务就永远修不好。
type ImageChunkPutPayload struct {
	TaskID string `json:"task_id"`
	// Offset 是本块在归档中的起始字节偏移。
	Offset int64 `json:"offset"`
	// DataBase64 是本块字节的 base64 编码（单块上限 256 KiB）。
	DataBase64 string `json:"data_base64"`
}

// ImageChunkPutResult 是一次上传的应答。
type ImageChunkPutResult struct {
	TaskID string `json:"task_id"`
	// ReceivedBytes 是本次写入完成后累计已收到的字节数。
	//
	// 返回累计值而不是"本块写入量"，是为了让 Hub 在重试后
	// 能直接确认 Agent 眼中的进度，不必自己累加（自己累加会与重试语义打架）。
	ReceivedBytes int64 `json:"received_bytes"`
	// Duplicate 为真表示这次是幂等重试（内容相同，实际没有写入）。
	Duplicate bool `json:"duplicate,omitempty"`
}

// ImageLoadCommitPayload 提交导入。
//
// 提交前 Agent 会校验**确切大小**与**最终 SHA256**，都通过后才执行 load。
// 校验通过后**异步**执行，立即返回任务对象（大归档的 load 可能跑很久，
// 不能占住请求）。
//
// SHA256 的来源规则（必须至少满足一条，且**不允许悄悄更正**）：
//   - begin 时已给出 → commit 可以留空，沿用 begin 的值；
//   - begin 时为空 → commit **必须**给出（Hub 边收边算的结果）。
//
// 两个都空 → 拒绝。没有校验和就没有手段确认最终字节与 Hub 算的是同一份，
// 一次错位的重试会静默产生损坏归档，而 load 可能"成功"出几个不完整的镜像。
//
// **两个都非空但不同 → 拒绝，不采用"以 commit 为准"的更正。**
// 理由：begin 的 SHA 通常来自**迁移源 Agent**（源侧 save 后算出的归档指纹），
// 它描述的是"这份归档本来应该是什么"。commit 的值是 Hub 边收边算的"实际收到了什么"。
// 两者不一致恰恰是最需要报出来的信号——说明传输中字节变了或分块错位。
// 若允许以 commit 为准悄悄覆盖，损坏的归档会被当成正常数据 load 进引擎，
// 而所有记录都显示"校验通过"。宁可让这次迁移失败并如实报告不一致。
type ImageLoadCommitPayload struct {
	TaskID string `json:"task_id"`
	// SHA256 是 Hub 边接收边算出的归档校验和，可空（空则沿用 begin 的值）。
	SHA256 string `json:"sha256,omitempty"`
}

// ImageChunkGetPayload 读取一块归档字节（save 方向）。
//
// Offset 与 MaxBytes 都必须显式给出：默认值会让"读到哪了"变得靠猜。
// MaxBytes 有硬上限（256 KiB），超过按上限截断而不是报错——
// 让 Hub 用同一个固定块大小反复调用是最简单的用法。
type ImageChunkGetPayload struct {
	TaskID string `json:"task_id"`
	Offset int64  `json:"offset"`
	// MaxBytes 期望读取的最大字节数；<=0 时按默认块大小处理。
	MaxBytes int64 `json:"max_bytes"`
}

// ImageChunkGetResult 是读取一块归档的应答。
type ImageChunkGetResult struct {
	TaskID string `json:"task_id"`
	// DataBase64 是本块字节；到末尾时为空串。
	DataBase64 string `json:"data_base64"`
	// Offset 是本块在归档中的起始偏移（回显，便于 Hub 校验不错位）。
	Offset int64 `json:"offset"`
	// EOF 为真表示已经读到归档末尾（本块可能是最后一块）。
	EOF bool `json:"eof"`
	// SizeBytes 是归档总大小，便于 Hub 校验进度与做完整性检查。
	SizeBytes int64 `json:"size_bytes"`
}

// ImageTaskPayload 是按任务 id 操作的通用载荷（status / cancel / release）。
type ImageTaskPayload struct {
	TaskID string `json:"task_id"`
}

// ImageTaskReleaseResult 是释放任务的结果。
//
// Released=false 表示任务还存在（例如正在执行，无法释放），
// 这不是错误——Hub 可以稍后再试。用布尔而不是错误码，
// 是因为"暂时不能释放"是正常的时序，不该在界面上报一次红。
type ImageTaskReleaseResult struct {
	TaskID   string `json:"task_id"`
	Released bool   `json:"released"`
	// Message 说明为什么没能释放（Released=false 时）。
	Message string `json:"message,omitempty"`
}

// ImageTaskCancelResult 是取消任务的结果。
//
// 重要边界：Canceled=true 只表示"取消了这次操作"，
// **不代表系统状态没变**。load 方向可能已经写入了部分镜像层，
// 影像入库没有事务可回滚，界面必须如实说明，不能宣称"已回滚"。
type ImageTaskCancelResult struct {
	TaskID   string `json:"task_id"`
	Canceled bool   `json:"canceled"`
	// Message 说明取消的实际影响（尤其是 load 可能已写入部分镜像）。
	Message string `json:"message,omitempty"`
}

// ImageTransferLimits 描述 Agent 侧的传输限额，供 Hub/前端展示。
//
// 把它作为任务字段回传，而不是让前端硬编码一串数字：
// 限额是可配置的、也可能随磁盘变化，硬编码必然与实际不符。
type ImageTransferLimits struct {
	// MaxArchiveBytes 单归档上限（默认 2 GiB）。
	MaxArchiveBytes int64 `json:"max_archive_bytes"`
	// MaxTotalArchiveBytes 活动归档总量上限（默认 4 GiB）。
	MaxTotalArchiveBytes int64 `json:"max_total_archive_bytes"`
	// MaxConcurrentTasks 并发任务上限（默认 2）。
	MaxConcurrentTasks int `json:"max_concurrent_tasks"`
	// ChunkSize 单块最大字节数（256 KiB）。
	ChunkSize int64 `json:"chunk_size"`
}

// ============================ Hub 侧上传端点的两条硬约束 ============================
//
// 以下不是 Go 侧代码，而是给 Hub 上传端点（浏览器直传）的契约说明，
// 写在这里是因为它们与 ImageLoadBeginPayload/ImageChunkPutPayload 的语义
// 绑在一起，放在两处容易走样：
//
//  1. **长度必须可知**，来源有两种，任一即可：
//     - HTTP 的 `Content-Length`；或
//     - 请求头 `X-Archive-Size`。
//     两者都缺失 → **拒绝并返回 411**（Length Required），
//     不做"边读边算、读到哪算哪"。
//     若两者都给出，服务端必须校验 **Hub 实际收到的字节数**同时等于它们，
//     不能只看声明值。
//
//     理由：没有长度就无法在接收前判断配额，也无法确认收到的字节数
//     与 load.begin 声明的一致。
//
//  2. **SHA256 由 Hub 边接收边增量计算**，在 commit 时提交。
//     **不要求浏览器侧计算**：`<input type=file>` 的文件对象可以直接上传，
//     Web 端不需要额外扫描一遍文件（那会让大归档的等待翻倍）。
//     Hub 在接收字节流的过程中维护哈希上下文，天然就是流式，
//     既不整包进内存，也不需要先落盘再算。
//
//     这与 ImageLoadBeginPayload.SHA256 允许为空是同一件事的两面：
//     把校验和推迟到 commit，接收端才能边收边算。
//
// 首批不支持 digest / platform 的界面入口，但协议保留并严格校验，见 ImagePullPayload。

// ============================ 可复用归档导出（编排迁移用） ============================
//
// 上面 ImageSave 的设计前提是"导出 → 立刻被同一个任务拉走 → 释放"。
// 编排迁移不满足这个前提：必须**先确认源服务器上的镜像能导出**，
// 再去目标服务器上做别的事，最后才回头把归档拉过来。
//
// 若复用 ImageSave，这段间隔里归档随时可能被 TTL 清理或任务释放，
// 迁移就会在最不可控的时刻失败。因此需要一层"可复用导出"：
//
//	image.archive.export  → 导出并**持有**（拿到 ExportID）
//	image.archive.open    → 再次确认它仍然可用（变更/越权检测）
//	image.archive.chunk.get → 按 offset 分块读取
//	image.archive.close   → 显式释放
//
// ============================ ExportID 的语义 ============================
//
// ExportID **不是** TaskID。两者刻意的区别：
//
//	TaskID   描述"一次导出操作"——有进度、可取消、终态后按 TTL 消失
//	ExportID 描述"一份落盘的归档"——有大小与校验和，只在显式 close 或过期时消失
//
// 一次导出操作产出一个 ExportID；但持有期由 ExportID 自己的 TTL 决定，
// 与任务的 TTL 解耦。这正是"可复用"的含义：任务结束后归档还在。
//
// ExportID 由 Agent 派生为 "<32 位小写 hex>"（任务 id 的形态），
// 因此同样能复用现有的 validTaskID 路径校验——**不引入新的路径拼装规则**。
// 任何不符合该形态的 ExportID 一律当作"不存在"，绝不进入文件系统路径。

// ImageArchiveExportPayload 请求导出一个可复用的镜像归档。
//
// Hold 语义：
//   - true  → 导出完成后**不自动释放**，归档保留到显式 close 或 TTL 过期；
//   - false → 与旧的 ImageSave 等价（仅供内部兼容，迁移场景必须传 true）。
//
// 为什么用显式布尔而不是"默认持有"：默认持有意味着调用方忘了 close
// 就会长期占着几百 MB 到 2 GiB 磁盘，而这个后果在界面上不可见。
// 反过来"默认不持有"最多是调用方多传一个字段。
type ImageArchiveExportPayload struct {
	// Reference 要导出的镜像引用或镜像 ID。
	Reference string `json:"reference"`
	// Hold 为真表示导出后保留归档，等待显式释放。
	Hold bool `json:"hold"`
}

// ImageArchiveExportResult 是一次可复用导出的结果。
//
// 返回**规范化后的引用**（ExportID）而不是复用 TaskID：
// 调用方后续只靠 ExportID 操作归档，不需要记住内部任务的存在。
type ImageArchiveExportResult struct {
	// ExportID 是这份归档的稳定标识（32 位小写十六进制）。
	ExportID string `json:"export_id"`
	// Reference 回显规范化后的镜像引用，便于调用方确认导出的是哪个镜像。
	Reference string `json:"reference"`
	// SizeBytes 是归档字节数，必定 > 0。
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 是归档的十六进制校验和，**必定非空**。
	//
	// 与 ImageLoadBeginPayload.SHA256 允许为空不同：这里的归档已经完整落盘，
	// 校验和是"这份文件到底是什么"的唯一凭证，拿不到就不该宣布导出成功。
	SHA256 string `json:"sha256"`
	// ExpiresAt 是归档的过期时间（RFC3339 UTC）。
	//
	// 给出过期时间而不是只给 TTL：调用方可能跨进程/跨小时规划迁移，
	// 让它自己减一次 TTL 只会引入时区与时钟偏差的误解。
	ExpiresAt string `json:"expires_at"`
}

// ImageArchiveOpenPayload 打开（确认可用）一个已有的归档。
//
// 为什么在 chunk.get 之外还要一个 open：
//
//	chunk.get 只回答"这一块是什么"，无法回答"这份归档还是不是原来那份"。
//	迁移流程在长时间等待后回头拉取时，需要先确认 ExportID 仍然存在、
//	大小与校验和未变——否则可能把一个**已过期被别的导出复用**的
//	identifer 当成原归档，悄悄传走错误的数据。
type ImageArchiveOpenPayload struct {
	ExportID string `json:"export_id"`
}

// ImageArchiveOpenResult 是 open 的应答。
//
// 字段与 ExportResult 保持同名同义：调用方可以用同一段代码处理
// "刚导出"与"稍后再打开"两种结果，不必写两套解析。
type ImageArchiveOpenResult struct {
	ExportID  string `json:"export_id"`
	Reference string `json:"reference"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	ExpiresAt string `json:"expires_at"`
}

// ImageArchiveChunkGetPayload 读取可复用归档的一块字节。
//
// 与 ImageChunkGetPayload 的区别仅在"用 ExportID 而不是 TaskID 定位"：
// 前者描述任务，后者描述归档。其余语义（offset 必须显式、max_bytes
// 超上限按上限截断）完全一致，响应直接复用 ImageChunkGetResult，
// 避免 Hub 侧出现两套几乎相同却细节不同的解析。
type ImageArchiveChunkGetPayload struct {
	ExportID string `json:"export_id"`
	// Offset 是本块在归档中的起始字节偏移，必须 >= 0。
	Offset int64 `json:"offset"`
	// MaxBytes 期望读取的最大字节数；<=0 时按默认块大小处理，
	// 超过上限按上限截断。
	MaxBytes int64 `json:"max_bytes"`
}

// ImageArchiveClosePayload 释放一个可复用归档。
type ImageArchiveClosePayload struct {
	ExportID string `json:"export_id"`
}

// ImageArchiveCloseResult 是释放的应答。
//
// Released=false 表示"这次没有删除任何东西"，通常因为归档已经不存在
// （已关闭或已过期）。这**不是错误**：
// 关闭一个已经不存在的归档，调用方要的结果（它不再占盘）已经达成，
// 报错只会让上层写一段"吞掉 not_found"的代码。
// 但 Message 必须如实说明是哪种情况，不能让调用方以为刚删掉了一份大文件。
type ImageArchiveCloseResult struct {
	ExportID string `json:"export_id"`
	Released bool   `json:"released"`
	// Message 说明实际发生了什么（已释放 / 本就不存在）。
	Message string `json:"message,omitempty"`
}
