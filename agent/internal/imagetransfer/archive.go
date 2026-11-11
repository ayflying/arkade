package imagetransfer

// archive.go 实现**可复用归档导出**：导出后持有归档，直到显式关闭或过期。
//
// ============================ 与 Save 的区别 ============================
//
// Save 产出的归档绑定在**任务**上：任务终态后按 SaveArchiveTTL 消失。
// 这对"导出完马上拉走"的流程完全够用，但编排迁移不是那个形状：
//
//	1. 在源服务器导出（几分钟，可能失败重试）；
//	2. 在目标服务器做一系列变更（几十分钟，中间可能失败回滚）；
//	3. 回头把归档拉过来。
//
// 第 1 步与第 3 步之间可能隔很久，而那时任务早已终态。
// 若复用 Save，归档会在第 3 步之前被 TTL 清掉，迁移在**最不可控**的
// 时刻失败；更糟的是失败现场表现为"任务不存在"，看起来像调用方写错了。
//
// 因此这里把"归档"从"任务"里独立出来：
//
//	ExportID 持有归档，有自己的 TTL 与读者计数；
//	任务只是**产生**它的一次性操作。
//
// ============================ 复用而不是另起一套 ============================
//
// 归档的物理位置、权限、symlink 防护全部复用 storage（它是唯一
// 决定"transfers 目录怎么用"的地方）。本文件只负责：
//   - 导出时把导出的产物登记成一份 export；
//   - 读/关/过期时维护这份登记。
//
// 关键实现选择：**导出直接产出 export，不先建 Task 再转手**。
// 早期想过"复用一个内部 Task 做导出，完成后把归档移交出去"，
// 但那样要么在移交时改名（与正在写的文件句柄打架），
// 要么留下一个"任务已释放但归档还在"的幽灵状态，
// 两种都会让 readers/writers 的记账变得难以推理。
// 这里直接让 export 自己就是一个可读的登记项，记账简单且不会漂移。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

// 可复用归档的 TTL 与限额。
const (
	// ExportTTL 是可复用归档的保留时长。
	//
	// 比 SaveArchiveTTL（30 分钟）长得多，因为它的用途就是"跨阶段持有"：
	// 一次编排迁移从导出到真正拉取，几十分钟到一小时都很正常。
	// 但也不能无限：一份 2 GiB 的归档长期占盘会挤掉后续所有导出。
	//
	// 2 小时是刻意的折中——足够覆盖一次完整迁移，又不至于让
	// 忘关的归档活过一整个工作日。
	ExportTTL = 2 * time.Hour
	// MaxExports 是同时持有的归档数量上限。
	//
	// 与 MaxConcurrentTasks 分开：持有归档**不占用并发**（没有 goroutine
	// 在跑），但每个都可能占 2 GiB，因此数量必须单独限死。
	// 8 个 × 2 GiB 在最坏情况下会顶到总量配额，实际由配额先拦住。
	MaxExports = 8
)

// 可复用归档的领域错误。
var (
	// ErrExportNotFound 归档不存在（从未导出、已关闭或已过期）。
	//
	// 三种情况刻意**合并成同一个错误**：对外它们的处理方式完全一致
	// （放弃这次迁移并重新导出），而区分它们只会让调用方写三段
	// 结果相同的分支。需要区分时看 Close 返回的 Message。
	ErrExportNotFound = errors.New("归档不存在或已释放")
	// ErrExportClosed 归档已被显式关闭。
	ErrExportClosed = errors.New("归档已关闭")
	// ErrExportExpired 归档超过保留时长已被清理。
	ErrExportExpired = errors.New("归档已过期")
	// ErrTooManyExports 同时持有的归档数量已达上限。
	ErrTooManyExports = errors.New("同时持有的归档数量已达上限")
	// ErrExportNotReady 归档仍在导出中，尚不可读。
	ErrExportNotReady = errors.New("归档尚未导出完成")
)

// ExportInfo 是一份可复用归档的对外只读信息。
//
// 只含调用方需要的字段：**不含本机路径**（那会暴露宿主机目录结构）。
type ExportInfo struct {
	ExportID  string
	Reference string
	SizeBytes int64
	SHA256    string
	ExpiresAt time.Time
}

// exportEntry 是一份持有中的归档。
type exportEntry struct {
	id        string
	reference string
	size      int64
	sha256    string
	createdAt time.Time
	expiresAt time.Time

	// readers 记录正在读取该归档的并发数，与 Task.readers 同一个目的：
	// 挡住"正在被读时删除文件"。POSIX 上 unlink 不会让已打开的句柄失效，
	// 于是读者会拿到"看起来正常但已不在磁盘上"的数据，
	// 下一次重试就 404——断裂位置完全不可预测。
	readers int32

	// gone 表示已关闭/已过期，任何新读取必须立即失败。
	gone bool
}

// exportRegistry 持有全部可复用归档。
//
// 为什么单独一个结构而不是塞进 Manager.tasks：
// 两者生命周期完全不同（任务是一次性操作，归档是持有物），
// 混在一张表里会让"任务表清理"与"归档过期"互相影响，
// 而这类耦合导致的 bug 极难复现（取决于两者 TTL 的相对大小）。
type exportRegistry struct {
	mu      sync.Mutex
	entries map[string]*exportEntry
	now     func() time.Time
}

func newExportRegistry(now func() time.Time) *exportRegistry {
	if now == nil {
		now = time.Now
	}
	return &exportRegistry{entries: make(map[string]*exportEntry), now: now}
}

// register 登记一份新归档。
//
// 调用方已持有归档文件，这里只做记账。数量上限与配额在 Manager 层判断。
func (r *exportRegistry) register(id, reference string, size int64, sha string) (ExportInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[id]; exists {
		// id 由随机数派生，撞车等于随机源出问题：拒绝而不是覆盖，
		// 覆盖会让正在读旧归档的调用方悄悄读到新内容。
		return ExportInfo{}, errors.New("归档标识重复，请重试")
	}
	now := r.now()
	e := &exportEntry{
		id: id, reference: reference, size: size, sha256: sha,
		createdAt: now, expiresAt: now.Add(ExportTTL),
	}
	r.entries[id] = e
	return e.info(), nil
}

// info 生成对外信息（调用方必须已持锁）。
func (e *exportEntry) info() ExportInfo {
	return ExportInfo{
		ExportID: e.id, Reference: e.reference, SizeBytes: e.size,
		SHA256: e.sha256, ExpiresAt: e.expiresAt,
	}
}

// lookup 取出可读的归档并登记一次读（必须与 endRead 配对）。
//
// 过期判定放在这里（访问时校验），而不是只靠后台清扫：
// 后台清扫是"尽力而为"的，若定时器被拖慢或进程刚启动还没跑过一轮，
// 过期归档就会继续被读到——**读出已过期数据比直接报错更危险**，
// 因为调用方会把它当成有效归档继续迁移。
func (r *exportRegistry) lookup(id string) (*exportEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[id]
	if !ok {
		return nil, ErrExportNotFound
	}
	if e.gone {
		return nil, ErrExportClosed
	}
	if r.now().After(e.expiresAt) {
		// 就地标记为过期：文件的实际删除交给 sweep/dispose，
		// 这里只保证"不再被读到"。
		e.gone = true
		delete(r.entries, id)
		return nil, ErrExportExpired
	}
	e.readers++
	return e, nil
}

// endRead 结束一次读登记。
func (r *exportRegistry) endRead(e *exportEntry) {
	r.mu.Lock()
	if e.readers > 0 {
		e.readers--
	}
	r.mu.Unlock()
}

// closeEntry 把归档标记为关闭并移出表，返回它（供删文件）。
//
// 返回 (nil, false) 表示本来就不存在——这不是错误，见 CloseExport 的说明。
func (r *exportRegistry) closeEntry(id string) (*exportEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	e, ok := r.entries[id]
	if !ok {
		return nil, false
	}
	e.gone = true
	delete(r.entries, id)
	return e, true
}

// sweepExpired 移除全部过期归档，返回被移除的条目（调用方负责删文件）。
func (r *exportRegistry) sweepExpired() []*exportEntry {
	now := r.now()
	var out []*exportEntry

	r.mu.Lock()
	for id, e := range r.entries {
		if now.After(e.expiresAt) {
			e.gone = true
			delete(r.entries, id)
			out = append(out, e)
		}
	}
	r.mu.Unlock()
	return out
}

// activeIDs 返回全部持有中的归档 id（供启动清理判断"哪些目录不该删"）。
func (r *exportRegistry) activeIDs() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.entries))
	for id := range r.entries {
		out[id] = true
	}
	return out
}

// count 返回持有中的归档数量。
func (r *exportRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

// totalBytes 返回持有中的归档总字节数（用于配额判断）。
func (r *exportRegistry) totalBytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	var total int64
	for _, e := range r.entries {
		total += e.size
	}
	return total
}

// waitReadersIdle 等该归档的在飞读结束，返回是否等到。
//
// 轮询上限 2 秒：一次 chunk get 只有 256 KiB，远低于此。
// 超时就返回 false，让调用方如实报告"稍后重试"，
// 绝不谎称已释放。
func (r *exportRegistry) waitReadersIdle(e *exportEntry) bool {
	const (
		step    = 2 * time.Millisecond
		maxWait = 2 * time.Second
	)
	waited := time.Duration(0)
	for {
		r.mu.Lock()
		n := e.readers
		r.mu.Unlock()
		if n == 0 {
			return true
		}
		if waited >= maxWait {
			return false
		}
		time.Sleep(step)
		waited += step
	}
}

// ============================ Manager 对外接口 ============================

// ExportArchive 导出一个镜像并**持有**归档，返回 ExportID 与校验和。
//
// 复用现有 save 的产物格式与 storage（transfers/<id>/archive.tar），
// 因此读取路径与 GetChunk 完全一致，不必为可复用归档再写一套文件逻辑。
//
// 关键差异：这里**同步**完成导出后返回。
//
// 为什么不像 Save 那样异步：
//   - 迁移要拿 SizeBytes 与 SHA256 才能规划后续步骤，异步返回两个未知值
//     等于让调用方自己写轮询；
//   - 一次迁移最多几个镜像，同步等几分钟是可接受的（Hub 侧有超时兜底）；
//   - 同步还天然消除了"导出还在跑就被 open"这类时序问题。
//
// 代价：调用方必须自己设超时。Hub 侧的 Copy 流程本来就有 ctx 超时，符合预期。
func (m *Manager) ExportArchive(ctx context.Context, reference string) (ExportInfo, error) {
	if !m.rtOK {
		return ExportInfo{}, errUnsupported
	}

	// 先 inspect 拿声明大小：配额预检必须在落盘前完成，
	// 否则会先写满磁盘再报"超限"——破坏已经发生。
	detail, derr := m.rt.InspectImage(ctx, reference)
	if derr != nil {
		return ExportInfo{}, derr
	}
	if detail.Size > m.limits.MaxArchiveBytes {
		return ExportInfo{}, ErrArchiveTooLarge
	}

	// 数量与配额在同一个临界区里判断，避免两个并发导出都通过。
	m.mu.Lock()
	if m.exports.count() >= MaxExports {
		m.mu.Unlock()
		return ExportInfo{}, ErrTooManyExports
	}
	// 已持有归档的字节数 + 磁盘实存 + 本次声明大小，都不能突破总量上限。
	if used := m.exports.totalBytes() + m.store.usage() + detail.Size; used > m.limits.MaxTotalArchiveBytes {
		m.mu.Unlock()
		return ExportInfo{}, ErrQuotaExceeded
	}
	id := newTaskID()
	m.mu.Unlock()

	info, err := m.exportToFile(ctx, id, reference, detail.Size)
	if err != nil {
		// 失败要把半截目录清掉：它没有任何用处（不是完整归档，不能被 open），
		// 留着只会占配额，还会让下次清理多一个待处理项。
		_ = m.store.removeTask(id)
		return ExportInfo{}, err
	}
	return info, nil
}

// exportToFile 执行实际的落盘导出，成功后登记。
func (m *Manager) exportToFile(ctx context.Context, id, reference string, declared int64) (ExportInfo, error) {
	rc, serr := m.rt.SaveImage(ctx, reference)
	if serr != nil {
		return ExportInfo{}, serr
	}
	defer func() { _ = rc.Close() }()

	path, f, cerr := m.store.createArchive(id)
	if cerr != nil {
		return ExportInfo{}, cerr
	}
	_ = path // 路径不回给调用方（它会暴露宿主机目录结构）

	// 边写边算 SHA256：成功后无需再读一遍几 GB 的文件。
	h := sha256.New()
	written, werr := io.Copy(io.MultiWriter(f, h), rc)
	_ = rc.Close()

	syncErr := f.Sync()
	closeErr := f.Close()

	if werr != nil || syncErr != nil || closeErr != nil {
		switch {
		case errors.Is(werr, context.Canceled), errors.Is(werr, context.DeadlineExceeded):
			return ExportInfo{}, werr
		case errors.Is(werr, ErrQuotaExceeded):
			return ExportInfo{}, ErrQuotaExceeded
		}
		return ExportInfo{}, errors.New("写出归档失败（可能是磁盘空间不足或权限问题）")
	}
	// 硬限额复核：声明大小来自引擎，实际落盘量可能不同。
	// 这是**真正的**防线——上面的预检只是提前给出友好提示。
	if written > m.limits.MaxArchiveBytes {
		return ExportInfo{}, ErrArchiveTooLarge
	}
	if written == 0 {
		// 空归档不能算成功：它既不能 load，也没有校验意义。
		return ExportInfo{}, errors.New("导出的归档为空")
	}

	return m.exports.register(id, reference, written, hex.EncodeToString(h.Sum(nil)))
}

// OpenExport 确认一份归档仍然可用，返回其信息。
//
// 除"存在性"外还做一次**完整性核对**：文件必须仍在、大小与登记值一致。
// 为什么必须核对大小：transfers 目录可被同机器其它进程触碰，
// 一个被截断的归档若被当成完整归档拉走，会在目标端 load 时失败，
// 而那时已经花掉了整个传输时间。
func (m *Manager) OpenExport(ctx context.Context, exportID string) (ExportInfo, error) {
	if !m.rtOK {
		return ExportInfo{}, errUnsupported
	}
	e, err := m.exports.lookup(exportID)
	if err != nil {
		return ExportInfo{}, err
	}
	defer m.exports.endRead(e)

	f, size, oerr := m.store.openArchive(exportID)
	if oerr != nil {
		if errors.Is(oerr, ErrTaskNotFound) {
			// 登记还在但文件没了：说明被外部删掉了。
			// 报"不存在"而不是内部错误——调用方的正确反应是重新导出。
			return ExportInfo{}, ErrExportNotFound
		}
		return ExportInfo{}, oerr
	}
	_ = f.Close()

	if size != e.size {
		return ExportInfo{}, ErrChecksumMismatch
	}
	return e.info(), nil
}

// GetExportChunk 读取一份持有归档的一块字节。
//
// 返回 (base64 数据, 回显 offset, 是否 EOF, 归档总大小, error)。
// 语义与 GetChunk 完全一致：max<=0 用默认块大小，超上限按上限截断。
func (m *Manager) GetExportChunk(ctx context.Context, exportID string, offset, maxBytes int64) (string, int64, bool, int64, error) {
	if offset < 0 {
		return "", 0, false, 0, ErrBadOffset
	}
	e, err := m.exports.lookup(exportID)
	if err != nil {
		return "", 0, false, 0, err
	}
	defer m.exports.endRead(e)

	if maxBytes <= 0 {
		maxBytes = m.limits.ChunkSize
	}
	if maxBytes > m.limits.ChunkSize {
		// 超过上限按上限截断而不是报错：让调用方用固定块大小反复调用
		// 是最简单的用法，不该因为"要多了"就失败。
		maxBytes = m.limits.ChunkSize
	}

	f, size, oerr := m.store.openArchive(exportID)
	if oerr != nil {
		if errors.Is(oerr, ErrTaskNotFound) {
			return "", 0, false, 0, ErrExportNotFound
		}
		return "", 0, false, 0, oerr
	}
	defer func() { _ = f.Close() }()

	if offset >= size {
		return "", offset, true, size, nil
	}

	buf := make([]byte, maxBytes)
	n, rerr := f.ReadAt(buf, offset)
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return "", offset, false, size, rerr
	}
	eof := offset+int64(n) >= size
	return encodeBase64(buf[:n]), offset, eof, size, nil
}

// CloseExport 释放一份归档。
//
// 返回值：(released, message, error)。
// released=false 且 error=nil 表示**本来就不存在**（已关闭或已过期）——
// 这不是失败：调用方要的结果（它不再占盘）已经达成。
// 但 message 如实说明是哪种情况，不能让调用方以为刚删掉了一份大文件。
func (m *Manager) CloseExport(ctx context.Context, exportID string) (bool, string, error) {
	e, ok := m.exports.closeEntry(exportID)
	if !ok {
		return false, "归档不存在（可能已释放或已过期）", nil
	}
	// 等正在读的调用结束再删文件：否则读者会拿到一个"已 unlink 但可读"
	// 的句柄，读到看似正常却已不存在的字节。
	if !m.exports.waitReadersIdle(e) {
		return false, "归档仍被读取占用，请稍后重试", errors.New("归档仍被读取")
	}
	if err := m.store.removeTask(exportID); err != nil {
		return false, "删除归档失败，请稍后重试", err
	}
	return true, "已释放", nil
}

// SweepExports 清理过期归档，返回清理数量。
//
// 由后台定时器调用。之所以还要有它（而不只靠访问时校验）：
// 访问时校验只能挡住"被读到"，但**占盘**必须主动回收——
// 一个忘关的归档若没人访问，会一直占着 2 GiB 直到进程重启。
func (m *Manager) SweepExports() int {
	expired := m.exports.sweepExpired()
	removed := 0
	for _, e := range expired {
		// 过期归档的读者可能还在读（例如正在拉取时刚好过期）。
		// 等不到就跳过，交给下一轮——绝不能因为等不到就强行删，
		// 那会让正在传输的调用方拿到损坏数据。
		if !m.exports.waitReadersIdle(e) {
			continue
		}
		if err := m.store.removeTask(e.id); err == nil {
			removed++
		}
	}
	return removed
}
