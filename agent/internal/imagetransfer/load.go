package imagetransfer

// load.go 实现镜像导入：分块接收 → 校验 → 调用引擎 load。
//
// ============================ 状态机 ============================
//
//	begin        → running / receiving   等待上传
//	chunk.put    → running / receiving   累加字节（幂等）
//	commit       → running / verifying   校验大小与 SHA
//	             → running / loading     调用引擎
//	             → succeeded / failed / canceled
//
// 三个必须由锁保证的约束（都踩过）：
//
//  1. **同任务的 PutChunk 必须串行**（Task.ioMu）。Manager.mu 在 IO 期间
//     已释放，两个并发到达的块会都读到同一个 received 值，
//     都判定"正常追加"→ 同一块写两次 → 归档静默错位，两次调用都返回成功。
//     Hub 重试时（超时重发与原请求同时在飞）这非常常见。
//
//  2. **commit 必须在校验并 load 之前就把状态推进到 loading**，
//     否则校验期间又来了一个 PutChunk，会在"已经校验过的字节"后面
//     追加新内容，形成一个大小和哈希都对不上、却即将被 load 的归档。
//
//  3. **release/cancel 之后的写入必须失败**（gone 标记），
//     否则在 POSIX 上会往一个已 unlink 的文件里写：
//     写"成功"了，内容却永远不会被谁看到。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// BeginLoad 声明一次导入任务。
func (m *Manager) BeginLoad(ctx context.Context, sizeBytes int64, sha string) (Snapshot, error) {
	if !m.rtOK {
		return Snapshot{}, errUnsupported
	}
	// 没有确切大小就无法在写入前判断配额：
	// "传到 90% 才发现不够"会白白占掉配额，且用户已经等了很久。
	if sizeBytes <= 0 {
		return Snapshot{}, ErrSizeMismatch
	}
	if sizeBytes > m.limits.MaxArchiveBytes {
		return Snapshot{}, ErrArchiveTooLarge
	}
	sha = normalizeSHA(sha)
	if sha != "" && !validSHA(sha) {
		return Snapshot{}, errors.New("归档校验和格式不正确（应为 64 位十六进制）")
	}

	// 总量配额在锁内与建任务一起判断（见上面一处的说明）。
	// 此处不再单独预检，避免出现"检查了两次、两次结果不同"的窗口。

	m.mu.Lock()
	// 配额检查与建任务在**同一个临界区**内完成，
	// 否则两个并发 begin 会都读到"还有空间"然后都通过，
	// 最后一起撞穿配额（而且是写到一半才失败）。见 reservedBytesLocked。
	if used := m.reservedBytesLocked(); used+sizeBytes > m.limits.MaxTotalArchiveBytes {
		m.mu.Unlock()
		return Snapshot{}, ErrQuotaExceeded
	}
	t, err := m.acquireTaskLocked(OpLoad, "")
	if err != nil {
		m.mu.Unlock()
		return Snapshot{}, err
	}
	// ============================ 立刻建好归档文件 ============================
	//
	// 必须在 begin 阶段就把空归档文件建出来并把路径记到任务上，
	// 而不是等第一块数据到达再建。理由有三：
	//
	//  1. PutChunk 的 O_APPEND 写需要一个已存在的路径，没有它就只能
	//     在每次写入时现建——多一条"建了没建"的判断分支；
	//  2. **占位**：文件一建出来就占了配额，别的任务不会把这部分
	//     空间再算一遍（虽然当前是按 begin 声明的 size 记账，但空文件
	//     的存在让"这个任务确实占着一块地方"在磁盘上可见）；
	//  3. 权限位（0600）在创建时一次性设好，避免后续追加时忘了收紧。
	//
	// 创建失败就把任务撤掉：一个无法写入的任务没有任何意义，
	// 留着只会让调用方在上传时才发现问题。
	path, f, cerr := m.store.createArchive(t.ID)
	if cerr != nil {
		delete(m.tasks, t.ID)
		if t.cancel != nil {
			t.cancel()
		}
		m.mu.Unlock()
		return Snapshot{}, cerr
	}
	_ = f.Close()

	t.archivePath = path
	t.Status = StatusRunning
	t.Phase = "receiving"
	t.BytesTotal = sizeBytes
	t.SizeBytes = sizeBytes
	t.SHA256 = sha
	t.Message = "等待上传归档"
	snap := t.snap()
	// ============================ 释放创建时的 writer 登记 ============================
	//
	// acquireTaskLocked 把 writers 置为 1 是为了消除"启动前被释放"的窗口；
	// 但 load 在 begin 之后**没有后台 goroutine**（它在等 Hub 上传），
	// 所以创建动作本身到此结束，必须把这次登记归零。
	//
	// 否则：writers 永远停在 1 → Release 会一直等到超时 → 用户永远无法释放
	// 一个"什么都没做"的 begin 任务。
	//
	// 注意 endWrite 会关闭 done 通道（last 判断）。这对 load 是正确的：
	// "begin 的创建动作已完成"。
	m.mu.Unlock()
	m.endWrite(t)
	return snap, nil
}

// PutChunk 写入一块归档字节，返回累计已接收字节数与是否为幂等重试。
//
// ============================ offset 幂等规则 ============================
//
// 只接受两种写入：
//  1. offset == received → 正常追加；
//  2. offset < received，且该区间的字节**与已写入内容逐字节相同** → 视为重试。
//
// 其它（尤其 offset > received）一律拒绝：那说明中间有块丢失，
// 继续写只会得到错位的 tar。宁可让 Hub 重传，也不能产生垃圾归档。
//
// 为什么必须支持第 2 种：Hub 发出块后连接可能断在
// "Agent 已写入但应答没送达"的时刻。若重发时直接报错，这个任务就永远修不好。
func (m *Manager) PutChunk(ctx context.Context, taskID string, offset int64, data []byte) (int64, bool, error) {
	if int64(len(data)) > m.limits.ChunkSize {
		return 0, false, ErrChunkTooLarge
	}
	if offset < 0 {
		return 0, false, ErrBadOffset
	}

	// 先拿任务引用（不持 ioMu），做状态与偏移的快速判定。
	m.mu.Lock()
	t, ok := m.tasks[taskID]
	if !ok || t.gone {
		m.mu.Unlock()
		return 0, false, ErrTaskNotFound
	}
	if t.Operation != OpLoad || t.committed {
		m.mu.Unlock()
		return 0, false, ErrTaskState
	}
	if t.Status.terminal() {
		m.mu.Unlock()
		return 0, false, ErrTaskState
	}
	m.mu.Unlock()

	// ============================ 串行化点（关键） ============================
	//
	// 从这里到解锁 ioMu 之间，该任务没有任何其它写操作能插进来。
	// 所有"读 received → 比较 → 写入 → 更新 received"必须整体在锁内，
	// 否则并发块会看到同一个 received。
	t.ioMu.Lock()
	defer t.ioMu.Unlock()

	// 拿锁后**重新**校验状态：等待 ioMu 期间任务可能已被 commit/cancel/release。
	m.mu.Lock()
	if t.gone || t.committed || t.Status.terminal() {
		m.mu.Unlock()
		return 0, false, ErrTaskState
	}
	received := t.received
	declared := t.SizeBytes
	path := t.archivePath
	m.mu.Unlock()

	if offset > received {
		// 中间缺块：明确拒绝，并回传当前进度，让 Hub 从 received 处补齐。
		return received, false, ErrBadOffset
	}
	// 声明大小的硬约束：不允许写到超出声明大小，
	// 否则"实际字节数 == 声明值"的校验就失去意义。
	if offset+int64(len(data)) > declared {
		return received, false, ErrSizeMismatch
	}

	// 幂等重试：比对已写入的区间。
	if offset < received {
		same, cerr := m.chunkMatches(taskID, offset, data)
		if cerr != nil {
			return received, false, cerr
		}
		if !same {
			return received, false, ErrBadOffset
		}
		return received, true, nil
	}

	// 正常追加。
	if err := m.appendChunk(path, data); err != nil {
		m.mu.Lock()
		t.Status = StatusFailed
		t.Message = "写入归档失败（可能是磁盘空间不足）"
		t.updatedAt = m.now()
		m.mu.Unlock()
		return received, false, err
	}

	m.mu.Lock()
	t.received = offset + int64(len(data))
	t.BytesDone = t.received
	t.updatedAt = m.now()
	newReceived := t.received
	m.mu.Unlock()
	return newReceived, false, nil
}

// CommitLoad 校验并异步执行 load。
//
// 校验顺序（任一不满足即拒绝）：
//  1. 两个 SHA 都空 → 拒绝；
//  2. 两个都非空但不同 → 拒绝（**不允许悄悄更正**，见下）；
//  3. 已接收字节数必须 == 声明大小；
//  4. 对整份归档流式重算 SHA256 并比对；
//  5. 全部通过 → 异步 load。
func (m *Manager) CommitLoad(ctx context.Context, taskID, sha string) (Snapshot, error) {
	sha = normalizeSHA(sha)

	m.mu.Lock()
	t, ok := m.tasks[taskID]
	if !ok || t.gone {
		m.mu.Unlock()
		return Snapshot{}, ErrTaskNotFound
	}
	if t.Operation != OpLoad || t.committed {
		m.mu.Unlock()
		return Snapshot{}, ErrTaskState
	}
	if t.Status.terminal() {
		m.mu.Unlock()
		return Snapshot{}, ErrTaskState
	}

	declaredSHA := t.SHA256
	switch {
	case declaredSHA == "" && sha == "":
		m.mu.Unlock()
		return Snapshot{}, ErrMissingChecksum
	case declaredSHA != "" && sha != "" && declaredSHA != sha:
		// begin 的 SHA 通常来自迁移源 Agent（这份归档**本来应该是**什么），
		// commit 的是 Hub 边收边算的结果（**实际收到了**什么）。
		// 两者不同恰恰是最该报出来的信号：传输中字节变了或分块错位。
		// 若允许以 commit 为准悄悄覆盖，损坏的归档会被当成正常数据
		// load 进引擎，而所有记录都显示"校验通过"。
		m.mu.Unlock()
		return Snapshot{}, fmt.Errorf("%w: begin 与 commit 提供的校验和不一致", ErrChecksumMismatch)
	}

	expectSHA := declaredSHA
	if expectSHA == "" {
		expectSHA = sha
	}
	if !validSHA(expectSHA) {
		m.mu.Unlock()
		return Snapshot{}, ErrMissingChecksum
	}

	received := t.received
	declaredSize := t.SizeBytes
	// ============================ 先置 committed（关键） ============================
	//
	// 必须在**释放锁之前**就把 committed 置真。若等校验完再置，
	// 校验期间到达的 PutChunk 会合法地往归档尾部追加，
	// 于是校验的是 A 份字节、load 的却是 A+B——大小与哈希都对不上，
	// 但没有任何一处会报错。
	//
	// 同时把 Phase 推到 verifying，让界面立刻反映"正在校验"，
	// 而不是在校验的几十秒里一直显示"接收中"。
	t.committed = true
	t.Status = StatusRunning
	t.Phase = "verifying"
	t.SHA256 = expectSHA
	m.mu.Unlock()

	// 大小校验：不必读盘。
	if received != declaredSize {
		m.mu.Lock()
		t.Status = StatusFailed
		t.Message = fmt.Sprintf("收到的字节数（%d）与声明（%d）不一致", received, declaredSize)
		t.updatedAt = m.now()
		out := t.snap()
		m.mu.Unlock()
		return out, nil
	}

	// 哈希校验要读整份归档（可能几 GB），必须在锁外做。
	// beginRead 保证校验期间归档不会被 release 删掉。
	rt, ok := m.beginRead(taskID)
	if !ok {
		m.mu.Lock()
		t.Status = StatusFailed
		t.Message = "归档已被释放，无法完成校验"
		t.updatedAt = m.now()
		out := t.snap()
		m.mu.Unlock()
		return out, nil
	}
	actual, herr := m.hashArchive(taskID)
	m.endRead(rt)

	if herr != nil {
		m.mu.Lock()
		t.Status = StatusFailed
		t.Message = "读取归档以校验完整性失败"
		t.updatedAt = m.now()
		out := t.snap()
		m.mu.Unlock()
		return out, nil
	}
	if !strings.EqualFold(actual, expectSHA) {
		m.mu.Lock()
		t.Status = StatusFailed
		t.Message = "归档校验和不一致，已拒绝导入（文件可能在传输中损坏）"
		t.updatedAt = m.now()
		t.gone = true
		out := t.snap()
		m.mu.Unlock()
		_ = m.removeArchiveWhenIdle(t)
		return out, nil
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Snapshot{}, errors.New("镜像传输管理器已关闭")
	}
	t.Phase = "loading"
	t.updatedAt = m.now()
	out := t.snap()
	taskCtx2 := t.ctx
	// 重新登记 writer：acquireTaskLocked 的那次登记已在 begin 结束时归零，
	// 而这里要启动 load goroutine。必须在**持锁时**加回来，
	// 否则又会出现"go 之前被 release 删掉"的窗口。
	t.writers++
	// done 是一次性的（关闭后不能重置），因此给本次 load 换一个新的通道。
	t.done = make(chan struct{})
	t.doneOnce = sync.Once{}
	m.mu.Unlock()

	go func() {
		// writers 已在创建时登记（见 acquireTaskLocked 的说明），这里只归零。
		defer m.endWrite(t)
		m.runLoad(taskCtx2, t)
	}()

	return out, nil
}

// runLoad 执行引擎 load 并落状态。
func (m *Manager) runLoad(taskCtx context.Context, t *Task) {
	runCtx, runCancel := context.WithTimeout(taskCtx, TaskTimeout)
	defer runCancel()

	if t.cancel != nil {
		defer t.cancel()
	}

	// 归档读取由 beginRead/endRead 保护：期间 release 不会删掉它。
	rt, ok := m.beginRead(t.ID)
	if !ok {
		m.mu.Lock()
		t.Status = StatusFailed
		t.Message = "归档已被释放，无法导入"
		t.updatedAt = m.now()
		m.mu.Unlock()
		return
	}
	// 注意：**不用 defer**。下面成功后要调 removeArchiveWhenIdle，
	// 而它会等 readers 归零。若把 endRead 放在 defer 里，
	// 它会等到函数返回才执行，于是 removeArchiveWhenIdle 等的正是自己，
	// 必然耗时到 2s 超时，归档还可能遗留下来直到下次启动清理。
	// 所有返回路径都显式调用 endReadOnce（内部用 once 防重复）。
	var endOnce sync.Once
	endRead := func() { endOnce.Do(func() { m.endRead(rt) }) }
	defer endRead()

	f, size, oerr := m.store.openArchive(t.ID)
	if oerr != nil {
		endRead()
		m.mu.Lock()
		t.Status = StatusFailed
		t.Message = "读取归档失败"
		t.updatedAt = m.now()
		m.mu.Unlock()
		return
	}

	images, lerr := m.rt.LoadImage(runCtx, f, size)
	_ = f.Close()
	// 数据读完、文件已关 → 立刻释放读取登记，之后才能删归档。
	endRead()

	m.mu.Lock()
	t.updatedAt = m.now()
	switch {
	case lerr == nil:
		t.Status = StatusSucceeded
		t.Phase = "done"
		if len(images) > 0 {
			// 引擎报告了加载出的镜像，把它们作为引用回传。
			names := make([]string, 0, len(images))
			for _, img := range images {
				names = append(names, img.Tags...)
			}
			t.Reference = strings.Join(names, ", ")
		}
		t.Message = "镜像导入完成"
		t.gone = true
		// 成功后归档已无用，立刻删掉释出配额（readers 已归零，不必等）。
		m.mu.Unlock()
		_ = m.removeArchiveWhenIdle(t)
		return
	case errors.Is(taskCtx.Err(), context.Canceled):
		t.Status = StatusCanceled
		// **关键边界**：镜像层一旦入库没有事务，取消无法回滚。
		// 必须如实说明，不能宣称"已回滚"。
		t.Message = "导入已取消；注意镜像入库无法回滚，可能已写入部分镜像"
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		t.Status = StatusFailed
		t.Message = "导入超时（超过 30 分钟）"
	default:
		t.Status = StatusFailed
		t.Message = "导入失败，归档可能不是有效的镜像归档"
	}
	m.mu.Unlock()
}

// appendChunk 把一块数据追加到归档文件。
//
// 调用方必须已持有 t.ioMu：追加依赖"当前文件长度 == received"
// 这个不变式，而 O_APPEND 本身并不校验偏移。
func (m *Manager) appendChunk(path string, data []byte) error {
	if path == "" {
		return errors.New("归档路径未初始化")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, archiveMode)
	if err != nil {
		return err
	}
	if _, werr := f.Write(data); werr != nil {
		_ = f.Close()
		return werr
	}
	return f.Close()
}

// chunkMatches 判断 offset 处的已写入内容是否与 data 完全相同。
//
// 用"读出同样长度的旧内容再比较"实现：分块只有 256 KiB，
// 一次比较的开销可忽略，换来的是严格的幂等语义。
//
// 调用方必须已持有 t.ioMu（保证比较期间内容不会被追加改变）。
func (m *Manager) chunkMatches(taskID string, offset int64, data []byte) (bool, error) {
	f, size, err := m.store.openArchive(taskID)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	if offset+int64(len(data)) > size {
		return false, nil
	}
	old := make([]byte, len(data))
	if _, rerr := f.ReadAt(old, offset); rerr != nil && !errors.Is(rerr, io.EOF) {
		return false, rerr
	}
	for i := range old {
		if old[i] != data[i] {
			return false, nil
		}
	}
	return true, nil
}

// hashArchive 流式计算归档的 SHA256。
//
// 用流式而不是 ReadFile：归档可能几 GB，整包读进内存会直接打死 Agent。
func (m *Manager) hashArchive(taskID string) (string, error) {
	f, _, err := m.store.openArchive(taskID)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, cerr := io.Copy(h, f); cerr != nil {
		return "", cerr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// normalizeSHA 统一校验和格式（去空白、转小写）。
func normalizeSHA(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// validSHA 校验 64 位十六进制。
func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
