package imagetransfer

// transfer.go 定义镜像传输任务的领域类型与限额常量。
//
// ============================ 为什么必须有任务概念 ============================
//
// 镜像传输是**长任务 + 大字节**：
//   - pull 要几十秒到几分钟；
//   - save 先要落盘几 GB，再被分块拉走；
//   - load 要边收边写，可能几分钟。
//
// 如果让一次 RPC 从头等到尾，就会撞上 Hub 的请求超时，
// 而超时之后 Agent 其实还在跑——用户以为失败，实际几秒后镜像被换了。
// 因此全部异步化：RPC 立即返回 task，进度靠 status 轮询。
//
// 任务只绑定**本进程**：task_id 由 Agent 自己生成，
// 因此 cancel/release 只可能作用于本进程创建的任务，不存在越权路径。
//
// ============================ 为什么限额是硬编码默认值 ============================
//
// 传输会把几 GB 写到 data_dir。没限额时，两三个并发导入就能把
// 宿主机磁盘写满，连带把宿主机上**其它**服务一起拖死——
// 而这个 Agent 自己也会因为写不进文件而全面失败。
//
// 因此限额是**硬约束**，不是提示：超了直接拒绝，而不是"继续但给个警告"。

import (
	"context"
	"errors"
	"sync"
	"time"
)

// 传输限额默认值。可从配置覆盖，但默认值本身必须保守：
// 面向"一台机器上还跑着别的服务"的现实场景，而不是"磁盘随便用"。
const (
	// DefaultMaxArchiveBytes 单归档上限 2 GiB。
	DefaultMaxArchiveBytes int64 = 2 << 30
	// DefaultMaxTotalArchiveBytes 活动归档总量上限 4 GiB。
	DefaultMaxTotalArchiveBytes int64 = 4 << 30
	// DefaultMaxConcurrentTasks 最多同时 2 个任务。
	//
	// 定 2 而不是更大的理由：每个 load/save 都要占用磁盘带宽与引擎的
	// 镜像锁，并发再高只会互相拖慢，而磁盘峰值占用会成倍上升。
	DefaultMaxConcurrentTasks = 2
	// ChunkSize 单块字节数 256 KiB。
	//
	// 选 256 KiB 的理由：控制连接的入站消息上限是 8 MiB，
	// base64 编码膨胀 33%，所以 256 KiB 的块编码后约 340 KiB，
	// 距离上限有 20 倍余量——这保证了即使协议层将来加信封字段、
	// Hub 侧加了并发，单块也绝不会逼近断连阈值。
	ChunkSize int64 = 256 << 10
)

// 任务生命周期时间参数。
const (
	// SaveArchiveTTL 是 save 成功后归档的保留时长。
	//
	// 保留而不是立即删除：Hub 要分块拉取，可能跨越多次 RPC，
	// 断线还要能续传。30 分钟足够一次正常迁移，又不至于让
	// 几 GB 垃圾长期占着磁盘。
	SaveArchiveTTL = 30 * time.Minute
	// LoadBeginTTL 是 begin 之后必须完成上传的时限。
	//
	// 10 分钟比 SaveArchiveTTL 短：这条路径上磁盘里存的是**半截**归档，
	// 它没有任何价值（不能 load、不能续传到别的任务），
	// 所以越早清理越好。
	LoadBeginTTL = 10 * time.Minute
	// TaskTimeout 是单个任务的总超时。
	TaskTimeout = 30 * time.Minute
)

// 领域错误。handler 层负责把它们转成中文文案并回给 Hub。
var (
	// ErrTaskNotFound 任务不存在（或已被回收）。
	ErrTaskNotFound = errors.New("任务不存在")
	// ErrTooManyTasks 并发任务已达上限。
	ErrTooManyTasks = errors.New("并发传输任务已达上限")
	// ErrArchiveTooLarge 单归档超过上限。
	ErrArchiveTooLarge = errors.New("归档超过单次传输上限")
	// ErrQuotaExceeded 活动归档总量超过上限。
	ErrQuotaExceeded = errors.New("传输临时区已满")
	// ErrBadOffset 分块偏移不符合幂等规则。
	ErrBadOffset = errors.New("分块偏移不正确")
	// ErrChunkTooLarge 单块超过上限。
	ErrChunkTooLarge = errors.New("分块超过单块上限")
	// ErrSizeMismatch 收到的字节数与声明不符。
	ErrSizeMismatch = errors.New("收到的字节数与声明不一致")
	// ErrChecksumMismatch 校验和不一致。
	ErrChecksumMismatch = errors.New("归档校验和不一致")
	// ErrMissingChecksum 完全没有校验和可用。
	ErrMissingChecksum = errors.New("缺少归档校验和")
	// ErrTaskState 当前状态不允许该操作（例如对已完成任务继续写块）。
	ErrTaskState = errors.New("当前任务状态不允许该操作")
)

// Status 是任务状态。
type Status string

// 任务状态取值，与协议 envelope.ImageTaskXxx 常量一一对应。
const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

// terminal 判断状态是否终态。
func (s Status) terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCanceled
}

// Operation 是任务来源操作。
type Operation string

// 三种传输操作。
const (
	OpPull Operation = "pull"
	OpSave Operation = "save"
	OpLoad Operation = "load"
)

// Task 是一个传输任务的完整状态。
//
// 字段全部由 Manager 在锁内读写，外部通过 Snapshot 拿到副本：
// 直接把内部结构交出去，会让调用方在不经意间读到正在被修改的字段，
// 在并发下产生"读到半新半旧"的进度。
type Task struct {
	ID        string
	Operation Operation

	// Reference 是镜像引用（load 在成功后才有值）。
	Reference string

	Status  Status
	Phase   string
	Message string

	BytesDone  int64
	BytesTotal int64 // 0 表示未知（**不能当作确实为 0**）

	SHA256    string
	SizeBytes int64

	// archivePath 是归档在 transfers 目录下的路径。
	//
	// 非导出字段：它含本机绝对路径，绝不能出现在协议响应里
	// （那会把宿主机目录结构暴露给 Hub 与浏览器）。
	archivePath string

	// received 是 load 方向已接收的字节数，用于 offset 幂等判定。
	received int64

	// cancel 取消该任务的执行上下文。
	cancel func()

	// ctx 是任务级上下文：长操作（拉取/导入）必须挂在它上面，
	// 这样 cancel 才能真正中断底层 HTTP 请求，而不是只改一个状态字段。
	//
	// 不用请求的 ctx：请求在任务启动的那一刻就返回了，
	// 它的 ctx 随即被取消——若把长任务挂在请求 ctx 上，
	// 每个任务都会在开始后立刻自杀。
	ctx context.Context

	// ioMu 是**每任务**的 IO 互斥锁，串行化该任务的所有文件操作。
	//
	// ============================ 为什么必须有它 ============================
	//
	// Manager.mu 只在"读改字段"的短临界区持有，文件 IO 在锁外做
	// （否则慢磁盘会把 status 查询一起卡住）。
	// 但这样就出现了一个真实竞态：同一个 task_id 的两个 PutChunk
	// 可以并发到达（Hub 重试时尤其常见——超时重发与原请求同时在飞）。
	// 两个 goroutine 都看到 offset == received，都判定"正常追加"，
	// 于是**同一块被写入两次**，归档从此错位，而两次调用都返回成功。
	//
	// 用 Manager.mu 直接罩住 IO 也不行：那会把所有任务与 status 一起阻塞。
	// 因此每任务一把锁：同任务串行、跨任务并行，status 查询永不等待 IO。
	//
	// 锁序（严格单向，避免死锁）：ioMu 必须在 Manager.mu **之外**持有。
	// 也就是说：先锁 ioMu，做完 IO 再放 ioMu，然后才去锁 Manager.mu 改字段；
	// 绝不允许"持 Manager.mu 去拿 ioMu"。
	ioMu sync.Mutex

	// readers 记录正在读取该归档的任务内并发数（chunk get 与 hash 校验）。
	//
	// 与 ioMu 的分工：读取**不必**与读取互斥（并发读同一文件天然安全），
	// 但它们必须挡住"删除归档"。见 release/removeArchive 的说明。
	readers int32

	// writers 记录该任务当前正在运行的写操作数（save 落盘、load 引擎调用、pull）。
	//
	// 与 readers 同一个目的但方向相反：
	//   - readers 挡住"正在被读时删除归档"；
	//   - writers 挡住"正在写时宣布可释放"。
	//
	// 为什么需要 writers 而不仅是状态判断：状态是**事后**的——
	// 一个 save goroutine 可能已经把状态写成 succeeded，但此时它
	// 还在跑最后的 defer（比如删半截文件）。若 release 此刻进来，
	// 会与那个 defer 同时操作同一路径，产生"删除失败但状态已报成功"。
	// 用活动计数把"操作真正结束"与"状态变成终态"区分开。
	writers int32

	// done 在任务的操作 goroutine 完全退出时关闭。
	//
	// 与 writers 的区别：writers 是计数（可能多次进出），done 是**一次性**信号，
	// 用来让测试/清理逻辑确切地知道"这个任务再也不会动文件了"。
	// 只有需要它的场景（当前是 release 与测试）才等它，
	// 普通调用路径只看状态，不该被别人的 defer 拖住。
	done     chan struct{}
	doneOnce sync.Once

	// gone 表示该任务的归档已被删除或任务已被移出任务表。
	//
	// 一旦置位，任何新的读取/写入都必须立即失败，而不是继续操作一个
	// 已经被 unlink 的文件——在 POSIX 上 unlink 之后文件仍可写，
	// 于是会得到"写成功了但内容永远不会被谁看到"的静默黑洞。
	gone bool

	// createdAt / updatedAt 用于 TTL 与过期清理。
	createdAt time.Time
	updatedAt time.Time

	// committed 表示 load 已提交（进入 load 阶段），此后不再接受新分块。
	committed bool
}
