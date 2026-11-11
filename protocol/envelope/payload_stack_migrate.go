// Package envelope：编排迁移所需的协议载荷。
//
// ============================ 为什么迁移需要这么多种载荷 ============================
//
// 一次跨服务器迁移要搬三类东西，它们的体量与失败后果完全不同：
//
//  1. 编排配置（几 KB）      —— 直接放进信封，没有任何负担；
//  2. 容器镜像（几十 MB~几 GB）—— 复用 task-13 已建好的 image.archive.* 分块协议；
//  3. 数据卷（可能几十 GB）  —— 这里新增 volume.archive.* / volume.import.*。
//
// 三者**绝不能**共用一个"整包传输"的方法：控制链路的单条入站上限是 8 MiB，
// base64 还会再放大 33%。把卷塞进一个信封，等于让一次迁移必然撞上限。
// 因此卷与镜像一样走"分块 + 头尾两次短 RPC"的形状。
//
// ============================ 与 image.archive.* 的关系 ============================
//
// 两者结构刻意保持同构（export/open/chunk.get/close ↔ begin/chunk.put/commit），
// 这样 Hub 侧的复制代码可以只换方法名复用同一套完整性校验，
// 而不必为"卷"再写一份几乎相同却细节不同的实现——
// 那种重复迟早会出现一处漏了校验。
//
// 但两者**不能合并成一个方法**：镜像归档有"持有到显式关闭"的语义
// （编排迁移要先导出、再规划、最后拉取），卷归档是"导出即可读、读完即释放"，
// 生命周期不同。硬合并会逼其中一方接受不需要的复杂度。
package envelope

// 方法名常量定义在 method.go 里（与其余方法保持同一处），此处只定义载荷。
//
// 方法名与载荷分开写的原因：method.go 是"有哪些方法"的唯一清单，
// 排查"Agent 支持什么"时只需要看它；载荷文件则是"每个方法传什么"。
// 如果把常量散落在各个载荷文件里，那份清单就不存在了。

// VolumeArchiveExportPayload 请求导出并持有一个数据卷归档。
type VolumeArchiveExportPayload struct {
	// Volume 是**Docker 里真实存在的卷名**（已含项目前缀，如 demo_data）。
	//
	// ============================ 为什么不传编排名 + 卷键 ============================
	// 让 Agent 自己去推导卷名，意味着两端各有一份"compose 命名规则"的实现，
	// 而这条规则有 external / name: / 项目名规整等多个分支，实现迟早会漂移。
	// 由调用方（Hub 迁移编排器）在**源端**用一次卷识别得出真实卷名，
	// 再原样传给两端，两端都只认最终结果。
	Volume string `json:"volume"`
	// Project 是编排项目名，仅用于日志与错误文案的可读性。
	Project string `json:"project,omitempty"`
}

// VolumeArchiveExportResult 是一次卷归档导出的结果。
//
// 字段与 ImageArchiveExportResult 保持同名同义，便于 Hub 用同一套代码处理。
type VolumeArchiveExportResult struct {
	// ExportID 是这份卷归档的稳定标识（32 位小写十六进制）。
	ExportID string `json:"export_id"`
	// Volume 回显真实卷名。
	Volume string `json:"volume"`
	// SizeBytes 是归档字节数，必定 > 0。
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 是归档的十六进制校验和，必定非空。
	SHA256 string `json:"sha256"`
	// ExpiresAt 是过期时间（RFC3339 UTC）。
	ExpiresAt string `json:"expires_at"`
}

// VolumeArchiveOpenPayload 打开（确认可用）一个已有的卷归档。
type VolumeArchiveOpenPayload struct {
	ExportID string `json:"export_id"`
}

// VolumeArchiveOpenResult 是 open 的应答。
type VolumeArchiveOpenResult struct {
	ExportID  string `json:"export_id"`
	Volume    string `json:"volume"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	ExpiresAt string `json:"expires_at"`
}

// VolumeArchiveChunkGetPayload 读取卷归档的一块字节。
type VolumeArchiveChunkGetPayload struct {
	ExportID string `json:"export_id"`
	Offset   int64  `json:"offset"`
	MaxBytes int64  `json:"max_bytes"`
}

// VolumeArchiveClosePayload 释放卷归档。
type VolumeArchiveClosePayload struct {
	ExportID string `json:"export_id"`
}

// VolumeArchiveCloseResult 是释放的应答。
type VolumeArchiveCloseResult struct {
	ExportID string `json:"export_id"`
	// Released=false 表示本来就不存在（已关闭/已过期），**不是错误**。
	Released bool   `json:"released"`
	Message  string `json:"message,omitempty"`
}

// VolumeImportBeginPayload 在目标服务器上开始接收一个卷。
//
// ============================ 为什么必须先 begin ============================
// 分块上传前必须知道两件事才有意义：
//
//  1. **总大小**：没有它就不知道何时收完，也无法预检磁盘配额——
//     边写边发现空间不够，意味着把目标磁盘写满之后才报错；
//  2. **校验和**：没有它，收到的是一个"字节数对得上但内容未知"的 tar，
//     解包进卷之后才发现损坏，而那时原始数据已被覆盖。
//
// 因此 begin 要求调用方先把这两个值交出来（源端导出时已经算好了）。
type VolumeImportBeginPayload struct {
	// Volume 是目标卷名。不存在时由实现创建。
	Volume string `json:"volume"`
	// Project 仅用于错误文案。
	Project string `json:"project,omitempty"`
	// SizeBytes 是归档的确切字节数（必须 > 0）。
	SizeBytes int64 `json:"size_bytes"`
	// SHA256 是归档的十六进制校验和。
	//
	// 与镜像的 load.begin 不同，这里**必填**：卷导入是"覆盖式解包"，
	// 没有校验和就无从判断收到的东西是否完整，
	// 而半截 tar 解进卷里会留下一个看起来正常、实际缺文件的数据卷。
	SHA256 string `json:"sha256"`
	// TaskID 是调用方指定的导入任务标识，用于幂等重试。
	//
	// ============================ 为什么要调用方指定 ============================
	// 迁移中断后重试时，Hub 必须能判断"这个 task 是重试同一次导入"
	// 而不是一次新的导入。若由 Agent 自己生成 task_id，
	// 每次重试都会得到一个全新任务，上一次写了半截的临时文件永远无人清理，
	// 而且可能同时存在两个把数据往同一个卷里解包的进程。
	//
	// 传空表示不要求幂等（Agent 自动生成），仅建议在手工调试时使用。
	TaskID string `json:"task_id,omitempty"`
}

// VolumeImportBeginResult 是 begin 的应答。
type VolumeImportBeginResult struct {
	TaskID string `json:"task_id"`
	Volume string `json:"volume"`
	// SizeBytes 与 SHA256 回显，便于调用方核对是自己那一次。
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
	// ReceivedBytes 是已接收的字节数。
	//
	// ============================ 幂等重试靠它 ============================
	// 重试时若 AlreadyReused=true，这个值告诉调用方可以从哪个 offset 续传；
	// 为 0 表示从头开始。没有它，重试只能盲目从头传一遍，
	// 而"盲目从头"在部分分块已落盘的情况下会写出错乱的归档。
	ReceivedBytes int64 `json:"received_bytes"`
	// AlreadyReused 表示这次 begin 复用了一个已存在的同 task_id 导入。
	AlreadyReused bool `json:"already_reused"`
}

// VolumeImportChunkPutPayload 上传卷归档的一块字节。
type VolumeImportChunkPutPayload struct {
	TaskID string `json:"task_id"`
	Offset int64  `json:"offset"`
	// DataBase64 是分块内容；单块上限与其它 chunk 一致（256 KiB）。
	DataBase64 string `json:"data_base64"`
}

// VolumeImportChunkPutResult 是分块上传的应答。
type VolumeImportChunkPutResult struct {
	// ReceivedBytes 是**累计已接收**的字节数（不是本块长度）。
	//
	// 调用方用它与 offset+len(data) 比对，确认服务端没有少收。
	// 返回累计值而不是增量，是因为重试时增量无法区分"这一块写了两遍"。
	ReceivedBytes int64 `json:"received_bytes"`
}

// VolumeImportCommitPayload 提交导入。
type VolumeImportCommitPayload struct {
	TaskID string `json:"task_id"`
	// SHA256 由调用方再次给出，必须与 begin 时一致。
	//
	// 两次都要传不是冗余：它让"begin 时算错了校验和"这种情况
	// 在提交时暴露，而不是让一份校验和错的数据被解包进卷里。
	SHA256 string `json:"sha256"`
}

// VolumeImportStatusPayload 查询导入任务。
type VolumeImportStatusPayload struct {
	TaskID string `json:"task_id"`
}

// VolumeImportResult 是导入任务的状态快照。
//
// 与 ImageTask 结构同构：状态机、进度指针语义、消息由 Agent 撰写，
// 这样 Hub 侧可以复用已有的展示逻辑。
type VolumeImportResult struct {
	TaskID  string `json:"task_id"`
	Volume  string `json:"volume"`
	Status  string `json:"status"`
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	// Progress 为 nil 表示进度未知（**不要显示 0%**）。
	Progress *float64 `json:"progress,omitempty"`
	// BytesDone 是已接收/已解包的字节数。
	BytesDone int64 `json:"bytes_done"`
	// BytesTotal 为 nil 表示总量未知。
	BytesTotal *int64 `json:"bytes_total,omitempty"`
	// SHA256 仅在成功后填充。
	SHA256 string `json:"sha256,omitempty"`
}

// 卷导入任务状态取值（与 ImageTask 完全一致的状态机）。
const (
	VolumeImportPending   = "pending"
	VolumeImportRunning   = "running"
	VolumeImportSucceeded = "succeeded"
	VolumeImportFailed    = "failed"
	VolumeImportCanceled  = "canceled"
)

// ComposeImportPayload 把编排配置导入目标服务器。
//
// ============================ 为什么单独一个方法而不复用 up ============================
// up 的语义是"写配置并立刻启动"。迁移的最后一步要不要启动由
// start_after 决定，而默认值是 **false**（不启动）——
// 若复用 up，就得在"不启动"时绕过 up 自己写文件，
// 那条路径会漏掉 up 内部已有的哈希校验、env 排除、原子提交等保护。
//
// 因此这里提供一个"只落盘、不启动"的方法，内部复用同一套落盘逻辑。
type ComposeImportPayload struct {
	// Name 是编排项目名。
	Name string `json:"name"`
	// Config 是 compose.yml 全文。
	Config string `json:"config"`
	// Env 是 .env 全文，可为空。
	Env string `json:"env,omitempty"`
	// ExpectHash 是调用方计算的配置哈希。
	//
	// 目标端落盘前会自己再算一次并比对：不一致说明传输过程损坏
	// （或两端哈希算法不一致），此时**绝不能落盘**——
	// 覆盖一份内容不确定的配置，比不迁移更糟。
	ExpectHash string `json:"expect_hash,omitempty"`
	// Overwrite 允许覆盖目标上已存在的同名编排。
	//
	// **默认 false，且迁移流程永远传 false**：产品决策是"目标已有同名编排
	// 则拒绝并提示改名"。保留这个开关是为了将来可能有显式的"覆盖迁移"，
	// 但当前所有调用方都不该打开它。
	Overwrite bool `json:"overwrite,omitempty"`
}

// ComposeImportResult 是导入结果的应答。
type ComposeImportResult struct {
	Name string `json:"name"`
	// ConfigHash 是落盘后目标端算出的哈希。
	ConfigHash string `json:"config_hash"`
	// AlreadyExists 表示目标上本来就有同名编排（此时未做任何修改）。
	AlreadyExists bool `json:"already_exists,omitempty"`
}
