package migrate

// import.go：接收卷归档并解包进目标卷。
//
// ============================ 为什么必须先落临时文件再解包 ============================
//
// 直觉上"边收边往卷里解 tar"更省磁盘，但它有一个致命问题：
// **半截 tar 解进卷里，会留下一个看起来正常、实际缺文件的数据卷**。
// 用户看到"迁移成功"，启动服务后才发现数据不全——
// 而那时已经无法判断丢了什么。
//
// 因此这里严格三段：
//
//	1. 分块写入 import 临时区的 archive.tar
//	2. 全部收完后校验 sha256 与长度，**不符就绝不解包**
//	3. 校验通过后用一个临时容器 tar -xf 解进目标卷
//
// ============================ 幂等 ============================
//
// 迁移会因网络问题中断，Hub 重试时用**同一个 task_id** 再次 begin。
// 此时必须：
//   - 不新建任务（否则会同时有两个进程往同一个卷里写）；
//   - 保留已收到的字节并告诉调用方 offset，让它续传；
//   - 若上一次已经收完并校验通过，直接返回"可以 commit"。
//
// ============================ 目标卷已存在数据怎么办 ============================
//
// 目标卷一律 create-only：创建时附随机归属标签，再 inspect 核对。
// 已有同名卷不会获得本任务的标签，因此拒绝写入，不合并、不清空。
// 失败/取消后可能留下本任务创建的部分卷，保留给人工排查，绝不自动删除。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 导入任务的状态与限额。
const (
	// VolumeImportTTL 是导入任务（含已结束的）保留时长。
	VolumeImportTTL = 30 * time.Minute
	// MaxVolumeImports 是同时进行的导入任务上限。
	MaxVolumeImports = 4
	// importTaskTimeout 是单次导入的预算。
	importTaskTimeout = 2 * time.Hour
)

// 导入任务状态取值。
const (
	ImportPending   = "pending"
	ImportRunning   = "running"
	ImportSucceeded = "succeeded"
	ImportFailed    = "failed"
	ImportCanceled  = "canceled"
)

// 导入的领域错误。
var (
	// ErrImportNotFound 导入任务不存在。
	ErrImportNotFound = errors.New("导入任务不存在或已过期")
	// ErrTooManyImports 导入任务数达上限。
	ErrTooManyImports = errors.New("同时进行的卷导入任务已达上限")
	// ErrImportParamMismatch 同一 task_id 用于了不同的卷/大小/校验和。
	ErrImportParamMismatch = errors.New("该任务标识已用于不同的卷导入")
	// ErrImportState 任务当前状态不允许该操作。
	ErrImportState = errors.New("导入任务状态不允许此操作")
	// ErrChecksumMismatch 收到的内容校验和不符。
	ErrChecksumMismatch = errors.New("卷归档校验和不一致，已解包前中止")
	// ErrSizeMismatch 收到的字节数与声明不符。
	ErrSizeMismatch = errors.New("卷归档字节数与声明不一致")
)

// importTask 是一次卷导入。
type importTask struct {
	id      string
	volume  string
	project string
	size    int64
	sha256  string
	dir     string

	// ioMu 串行化该任务的文件操作。
	//
	// ============================ 锁顺序 ============================
	// 全局锁（Importer.mu）只保护任务表与字段，**绝不在持锁时做 IO**。
	// 每个任务自己一把 ioMu 保护它的临时文件。
	// 顺序严格单向：ioMu → IO → 释放 ioMu → mu。
	// 反过来（持 mu 时做 IO）会让一个慢磁盘拖死所有任务。
	ioMu sync.Mutex

	mu            sync.Mutex
	status        string
	phase         string
	message       string
	received      int64
	bytesTotal    int64
	errText       string
	sha256Done    string
	createdAt     time.Time
	finishedAt    time.Time
	cleanupFailed bool
	cancel        context.CancelFunc
}

// ImportSnapshot 是导入任务的一次快照。
type ImportSnapshot struct {
	TaskID     string
	Volume     string
	Status     string
	Phase      string
	Message    string
	Received   int64
	BytesTotal int64
	SHA256     string
	Error      string
}

// Importer 管理全部卷导入任务。
type Importer struct {
	root string
	// docker 执行 docker 命令（可被测试替换）。
	docker func(ctx context.Context, args ...string) ([]byte, error)
	now    func() time.Time

	mu   sync.Mutex
	task map[string]*importTask
}

// NewImporter 构造卷导入管理器。
func NewImporter(root string, docker func(ctx context.Context, args ...string) ([]byte, error)) *Importer {
	if docker == nil {
		docker = defaultDocker
	}
	return &Importer{root: root, docker: docker, now: time.Now, task: make(map[string]*importTask)}
}

// Begin 开始（或复用）一次卷导入。
//
// 返回 AlreadyReused 与 ReceivedBytes 让调用方知道从哪里续传。
func (m *Importer) Begin(ctx context.Context, volume, project string, size int64, sha string, taskID string) (ImportSnapshot, bool, error) {
	if !validVolumeName(volume) {
		return ImportSnapshot{}, false, fmt.Errorf("%w: %q", ErrBadVolumeName, volume)
	}
	if size <= 0 {
		return ImportSnapshot{}, false, fmt.Errorf("必须提供归档的确切字节数（size_bytes > 0）")
	}
	if size > MaxVolumeArchiveBytes {
		return ImportSnapshot{}, false, ErrVolumeArchiveTooLarge
	}
	if !isHexSHA256(sha) {
		return ImportSnapshot{}, false, fmt.Errorf("必须提供合法的 sha256（64 位十六进制）")
	}

	// ============================ 幂等：先查后建 ============================
	// taskID 由调用方给定，用于重试时复用同一任务。
	if taskID != "" {
		if !validVolumeName(taskID) {
			return ImportSnapshot{}, false, fmt.Errorf("任务标识不合法")
		}
		m.mu.Lock()
		prior, ok := m.task[taskID]
		if ok {
			// 同 id 必须同参数：不同参数说明是另一次导入，
			// 静默复用会让调用方以为新参数已生效。
			if prior.volume != volume || prior.project != project || prior.size != size || !equalHex(prior.sha256, sha) {
				m.mu.Unlock()
				return ImportSnapshot{}, false, ErrImportParamMismatch
			}
			snap := prior.snapshot()
			m.mu.Unlock()
			return snap, true, nil
		}
		m.mu.Unlock()
	}

	m.mu.Lock()
	// 清理过期任务（顺带，不引入后台定时器）。
	m.sweepLocked()
	live := 0
	for _, t := range m.task {
		t.mu.Lock()
		active := !importTerminal(t.status)
		t.mu.Unlock()
		if active {
			live++
		}
	}
	if live >= MaxVolumeImports {
		m.mu.Unlock()
		return ImportSnapshot{}, false, ErrTooManyImports
	}
	for _, prior := range m.task {
		prior.mu.Lock()
		live := !importTerminal(prior.status)
		prior.mu.Unlock()
		if live && prior.volume == volume {
			m.mu.Unlock()
			return ImportSnapshot{}, false, ErrTargetVolumeExists
		}
	}
	if taskID == "" {
		taskID = newExportID()
	}
	dir := filepath.Join(m.root, taskID)
	// Begin reservation is held through mkdir and table insertion. This is a short
	// metadata operation; no upload/engine IO is performed under the manager lock.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		m.mu.Unlock()
		return ImportSnapshot{}, false, fmt.Errorf("创建导入临时目录失败: %w", err)
	}
	if _, exists := m.task[taskID]; exists {
		// 并发 Begin 同一 id：另一个赢家已建好，复用它的。
		prior := m.task[taskID]
		if prior.volume != volume || prior.project != project || prior.size != size || !equalHex(prior.sha256, sha) {
			m.mu.Unlock()
			return ImportSnapshot{}, false, ErrImportParamMismatch
		}
		snap := prior.snapshot()
		m.mu.Unlock()
		return snap, true, nil
	}
	for _, prior := range m.task {
		prior.mu.Lock()
		live := !importTerminal(prior.status)
		prior.mu.Unlock()
		if live && prior.volume == volume {
			m.mu.Unlock()
			return ImportSnapshot{}, false, ErrTargetVolumeExists
		}
	}
	t := &importTask{
		id: taskID, volume: volume, project: project,
		size: size, sha256: sha, dir: dir,
		status: ImportPending, phase: "等待上传",
		message: "已准备接收卷归档", bytesTotal: size,
		createdAt: m.now(),
	}
	m.task[taskID] = t
	snap := t.snapshot()
	m.mu.Unlock()
	return snap, false, nil
}
