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
// 解包是**合并覆盖**（tar -xf 不会先清空）。这是刻意的：
//   - 目标卷若不存在则由 Docker 新建空卷，解包即完整恢复；
//   - 目标卷若已存在（例如目标上已有一个同名编排跑过），
//     合并比"清空再解"安全——清空是不可逆的破坏，而合并最多留下多余文件。
//
// 但这意味着**不能保证目标卷与源完全一致**（源已删除的文件在该卷里仍会存在）。
// 这是已知限制，必须在结果里如实告知，不能宣称"完全一致"。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

	mu         sync.Mutex
	status     string
	phase      string
	message    string
	received   int64
	bytesTotal int64
	errText    string
	sha256Done string
	createdAt  time.Time
	finishedAt time.Time
	cancel     context.CancelFunc
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
			if prior.volume != volume || prior.size != size || prior.sha256 != sha {
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
		if !importTerminal(t.status) {
			live++
		}
	}
	if live >= MaxVolumeImports {
		m.mu.Unlock()
		return ImportSnapshot{}, false, ErrTooManyImports
	}
	if taskID == "" {
		taskID = newExportID()
	}
	dir := filepath.Join(m.root, taskID)
	m.mu.Unlock()

	// 目录创建在锁外（IO 不进临界区）。
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ImportSnapshot{}, false, fmt.Errorf("创建导入临时目录失败: %w", err)
	}

	m.mu.Lock()
	if _, exists := m.task[taskID]; exists {
		// 并发 Begin 同一 id：另一个赢家已建好，复用它的。
		snap := m.task[taskID].snapshot()
		m.mu.Unlock()
		return snap, true, nil
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

// PutChunk 写入一块字节。
//
// offset 必须**正好等于已接收字节数**：允许任意 offset 会让"重传同一块"
// 变成静默的重复写入，最终文件长度正确但内容错乱，而校验和要到最后才报错。
// 只允许"从当前位置继续"这一个含义，语义最简单也最难出错。
func (m *Importer) PutChunk(ctx context.Context, taskID string, offset int64, data []byte) (int64, error) {
	if offset < 0 {
		return 0, ErrBadOffset
	}
	m.mu.Lock()
	t, ok := m.task[taskID]
	m.mu.Unlock()
	if !ok {
		return 0, ErrImportNotFound
	}

	t.mu.Lock()
	status := t.status
	current := t.received
	t.mu.Unlock()

	if importTerminal(status) {
		return current, ErrImportState
	}
	if status == ImportSucceeded {
		return current, ErrImportState
	}

	// ============================ 续传语义 ============================
	// offset < current：调用方在重发已收到的块。**幂等接受**，不重复写。
	//   —— 网络重试时 Hub 不知道上一次的 ack 是否到达，重发是正常的。
	// offset > current：中间缺块，必须拒绝——接受会造成空洞，
	//   而空洞的 tar 在解包时行为不可预测。
	if offset < current {
		return current, nil
	}
	if offset > current {
		return current, fmt.Errorf("分块偏移不连续（已收到 %d 字节，请求写入 offset=%d）", current, offset)
	}
	// 超过声明大小直接拒绝：不能指望最后由长度校验兜底，
	// 那意味着磁盘已经被写超了。
	if current+int64(len(data)) > t.size {
		return current, ErrSizeMismatch
	}

	t.ioMu.Lock()
	defer t.ioMu.Unlock()

	if status == ImportPending {
		t.mu.Lock()
		t.status = ImportRunning
		t.phase = "接收归档"
		t.mu.Unlock()
	}

	path := filepath.Join(t.dir, "archive.tar")
	// O_WRONLY|O_CREATE 不带 TRUNC：续传要保留已有内容。
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		t.fail("无法写入导入临时文件")
		return current, fmt.Errorf("写入导入临时文件失败: %w", err)
	}
	if _, err := f.WriteAt(data, offset); err != nil {
		_ = f.Close()
		t.fail("写入导入临时文件失败")
		return current, fmt.Errorf("写入导入临时文件失败: %w", err)
	}
	if err := f.Close(); err != nil {
		t.fail("写入导入临时文件失败")
		return current, fmt.Errorf("写入导入临时文件失败: %w", err)
	}

	t.mu.Lock()
	t.received = current + int64(len(data))
	done := t.received
	t.mu.Unlock()
	return done, nil
}

// Commit 校验并解包。
//
// ============================ 校验必须先于解包 ============================
// 顺序绝不能反：一旦解包开始，卷里就已经混入了可能损坏的数据。
// 因此这里先算 sha256、比对长度，全部通过才起容器解包。
func (m *Importer) Commit(ctx context.Context, taskID, sha string) (ImportSnapshot, error) {
	m.mu.Lock()
	t, ok := m.task[taskID]
	m.mu.Unlock()
	if !ok {
		return ImportSnapshot{}, ErrImportNotFound
	}

	t.mu.Lock()
	status := t.status
	received := t.received
	wantSize := t.size
	wantSHA := t.sha256
	t.mu.Unlock()

	if importTerminal(status) {
		return t.snapshot(), nil
	}
	if sha != "" && !equalHex(sha, wantSHA) {
		t.fail("提交的校验和与开始时声明的不一致")
		return t.snapshot(), ErrImportParamMismatch
	}
	if received != wantSize {
		t.fail("收到的字节数与声明不一致")
		return t.snapshot(), ErrSizeMismatch
	}

	t.ioMu.Lock()
	defer t.ioMu.Unlock()

	path := filepath.Join(t.dir, "archive.tar")
	got, size, err := hashFile(path)
	if err != nil {
		t.fail("读取导入临时文件失败")
		return t.snapshot(), err
	}
	if size != wantSize || !equalHex(got, wantSHA) {
		// 校验失败**不解包**：卷保持原样，用户数据未被污染。
		t.fail("卷归档校验和不一致，已中止导入（目标卷未被修改）")
		return t.snapshot(), ErrChecksumMismatch
	}

	t.mu.Lock()
	t.phase = "解包到数据卷"
	t.message = "校验通过，正在解包"
	t.mu.Unlock()

	// 解包：临时容器把归档解进目标卷。
	// 目标卷不存在时 docker run -v <name>:/to 会自动创建它。
	out, derr := m.docker(ctx, "run", "--rm",
		"-v", t.volume+":/to",
		"-v", t.dir+":/from:ro",
		"alpine:3.20",
		"tar", "-xf", "/from/archive.tar", "-C", "/to")

	t.mu.Lock()
	t.finishedAt = m.now()
	if derr != nil {
		t.status = ImportFailed
		t.phase = "失败"
		t.message = filterImportError(derr, string(out))
		t.errText = t.message
		t.mu.Unlock()
		return t.snapshot(), derr
	}
	t.status = ImportSucceeded
	t.phase = "已完成"
	t.message = "数据卷已恢复"
	t.sha256Done = wantSHA
	t.mu.Unlock()

	// 成功后才删临时文件：失败时保留，便于排查（由 TTL 清理兜底）。
	_ = os.Remove(path)
	return t.snapshot(), nil
}

// Status 查询任务快照。
func (m *Importer) Status(taskID string) (ImportSnapshot, error) {
	m.mu.Lock()
	m.sweepLocked()
	t, ok := m.task[taskID]
	m.mu.Unlock()
	if !ok {
		return ImportSnapshot{}, ErrImportNotFound
	}
	return t.snapshot(), nil
}

// sweepLocked 清理过期任务（调用方已持锁）。
func (m *Importer) sweepLocked() {
	now := m.now()
	for id, t := range m.task {
		t.mu.Lock()
		expired := importTerminal(t.status) && now.Sub(t.finishedAt) > VolumeImportTTL
		dir := t.dir
		t.mu.Unlock()
		if !expired {
			continue
		}
		delete(m.task, id)
		_ = os.RemoveAll(dir)
	}
}

// snapshot 生成快照。
func (t *importTask) snapshot() ImportSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return ImportSnapshot{
		TaskID: t.id, Volume: t.volume, Status: t.status,
		Phase: t.phase, Message: t.message,
		Received: t.received, BytesTotal: t.bytesTotal,
		SHA256: t.sha256Done, Error: t.errText,
	}
}

// fail 把任务置为失败。
func (t *importTask) fail(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if importTerminal(t.status) {
		return
	}
	t.status = ImportFailed
	t.phase = "失败"
	t.message = msg
	t.errText = msg
	t.finishedAt = time.Now()
}

// importTerminal 判断状态是否终态。
func importTerminal(s string) bool {
	return s == ImportSucceeded || s == ImportFailed || s == ImportCanceled
}

// isHexSHA256 校验 64 位十六进制。
//
// 注意这里的判断必须同时接受大小写：不同引擎/系统给出的校验和
// 大小写并不统一，只接受一种会把正确的归档判成非法。
func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// equalHex 大小写不敏感比较十六进制串。
func equalHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'F' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'F' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// filterImportError 把解包失败转成中文说明（过滤掉容器输出里的敏感内容）。
func filterImportError(err error, out string) string {
	msg := err.Error() + " " + out
	switch {
	case strings.Contains(msg, "No space left"):
		return "目标磁盘空间不足，解包失败"
	case strings.Contains(msg, "Permission denied"):
		return "无权限写入目标数据卷"
	case strings.Contains(msg, "Cannot open"), strings.Contains(msg, "not found in archive"):
		return "归档内容无法解包（可能不是有效的 tar）"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "解包超时或被取消"
	}
	return "解包数据卷失败（请查看服务器上的 docker 日志）"
}

// ReadArchiveFile 返回导入临时文件（仅测试与调试用）。
//
// 导出它的目的是让测试能验证"收到的字节确实落盘且内容正确"，
// 而不必真的起容器解包。
func (m *Importer) ReadArchiveFile(taskID string) ([]byte, error) {
	m.mu.Lock()
	t, ok := m.task[taskID]
	m.mu.Unlock()
	if !ok {
		return nil, ErrImportNotFound
	}
	return os.ReadFile(filepath.Join(t.dir, "archive.tar"))
}
