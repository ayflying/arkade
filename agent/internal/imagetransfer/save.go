package imagetransfer

// save.go 实现镜像导出与归档分块读出。
//
// ============================ 流式落盘 ============================
//
// 导出不是"先拿到整个 tar 再处理"——那是几百 MB 到几 GB 的流。
// 实现上把引擎返回的 ReadCloser 与磁盘文件、SHA256 哈希器
// 用 io.MultiWriter 串起来，边读边写边算：
//
//	引擎流 ──► MultiWriter(磁盘文件, SHA256)
//
// 这样内存占用与归档大小无关，且成功后无需再读一遍文件算校验和
// （那会让一个 2GB 的导出多等几十秒）。

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
)

// encodeBase64 是分块响应统一的编码方式。
//
// 抽成一个函数而不是各处写 base64.StdEncoding.EncodeToString：
// 一旦将来换成 URL-safe 或去掉填充，只有一处需要改。
func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// Save 启动一次导出，立即返回任务快照。
//
// 归档落在 transfers/<task_id>/archive.tar（0600），成功后保留到
// release 或 TTL 过期，供 Hub 分块拉取。
func (m *Manager) Save(ctx context.Context, reference string) (Snapshot, error) {
	if !m.rtOK {
		return Snapshot{}, errUnsupported
	}

	// 先确认镜像存在并拿到大小：配额预检必须在**创建任务之前**完成。
	// 否则任务会被创建又立刻失败，界面上出现一次无意义的闪烁。
	detail, derr := m.rt.InspectImage(ctx, reference)
	if derr != nil {
		return Snapshot{}, derr
	}
	if detail.Size > m.limits.MaxArchiveBytes {
		return Snapshot{}, ErrArchiveTooLarge
	}
	m.mu.Lock()
	// 配额检查与建任务在同一临界区（见 reservedBytesLocked 的说明）：
	// 分两次检查会留下"两次结果不同"的窗口，两个并发 Save 可能都通过。
	if used := m.reservedBytesLocked(); used+detail.Size > m.limits.MaxTotalArchiveBytes {
		m.mu.Unlock()
		return Snapshot{}, ErrQuotaExceeded
	}
	t, err := m.acquireTaskLocked(OpSave, reference)
	if err != nil {
		m.mu.Unlock()
		return Snapshot{}, err
	}
	t.Status = StatusRunning
	t.Phase = "saving"
	t.SizeBytes = detail.Size
	t.BytesTotal = detail.Size
	snap := t.snap()
	taskCtx, cancel := t.ctx, t.cancel
	m.mu.Unlock()

	go func() {
		defer cancel()
		// writers 已在 acquireTaskLocked 里置为 1（创建即登记，
		// 消除"启动前被 release 删掉归档"的窗口），这里只负责归零。
		defer m.endWrite(t)
		m.runSave(taskCtx, t, reference)
	}()

	return snap, nil
}

// runSave 执行导出并落状态。
func (m *Manager) runSave(taskCtx context.Context, t *Task, reference string) {
	runCtx, runCancel := context.WithTimeout(taskCtx, TaskTimeout)
	defer runCancel()

	// fail 统一处理失败：置状态 + 清理半截归档。
	// 半截归档没有任何用处（不能 load、不能续传到别的任务），
	// 留着只会占配额。
	fail := func(msg string) {
		m.mu.Lock()
		t.Status = StatusFailed
		t.Message = msg
		t.updatedAt = m.now()
		t.gone = true
		m.mu.Unlock()
		_ = m.removeArchiveWhenIdle(t)
	}

	rc, serr := m.rt.SaveImage(runCtx, reference)
	if serr != nil {
		fail("导出镜像失败，请确认镜像存在")
		return
	}
	defer func() { _ = rc.Close() }()

	// 持 ioMu 做文件创建后的全部写操作：防止与并发的 cancel/release
	// 以及任何同任务的读写交叉。详见 transfer.go 里 Task.ioMu 的说明。
	t.ioMu.Lock()

	path, f, cerr := m.store.createArchive(t.ID)
	if cerr != nil {
		t.ioMu.Unlock()
		fail("创建归档文件失败，请检查数据目录权限与磁盘空间")
		return
	}

	m.mu.Lock()
	t.archivePath = path
	m.mu.Unlock()

	// 边写边算 SHA256：成功后无需再读一遍几 GB 的文件。
	h := sha256.New()

	// ============================ 硬限额：写入前检查（不是事后检查） ============================
	//
	// 这里绝不能用 io.Copy(f, rc) 然后在事后比较大小：
	// 那样磁盘已经被**写满**了才报错，破坏已经发生。
	//
	// 三道闸门，全部在"写之前"判断：
	//   1. limitWriter：单归档上限。累计即将写入的量超过上限时，
	//      直接返回 ErrArchiveTooLarge **且不再向文件写入任何字节**。
	//      这是绝对边界——归档落盘量不会超过 MaxArchiveBytes。
	//   2. 总量预算：每次写之前核对 m.checkWriteBudget（含所有活动任务的
	//      实际占用与最坏增量），防止多任务叠加写满磁盘。
	//   3. ctx：每块之间检查取消/超时，让停止能真正生效。
	//
	// 用 io.CopyBuffer 而不是手写循环：缓冲区由我们提供（固定 1MiB，
	// 不随归档大小增长），而 CopyBuffer 会复用 rc 的 ReadFrom 优化……
	// 因此这里显式包装成不实现 ReaderFrom/WriterTo 的形态，
	// 保证走我们自己的 Write 路径（否则 limitWriter 的检查会被绕过）。
	lw := &limitWriter{
		dst:     f,
		hash:    h,
		limit:   m.limits.MaxArchiveBytes,
		written: 0,
		// 每块落盘前核对全局预算与 ctx。
		beforeWrite: func(n int64) error {
			select {
			case <-runCtx.Done():
				return runCtx.Err()
			default:
			}
			if berr := m.checkWriteBudget(t, n); berr != nil {
				return berr
			}
			return nil
		},
		// 进度回调：让导出过程中就可查，而不是全程停在 0。
		onProgress: func(n int64) {
			m.mu.Lock()
			t.BytesDone = n
			t.updatedAt = m.now()
			m.mu.Unlock()
		},
	}

	buf := make([]byte, saveCopyBufSize)
	werr := copyWithLimit(lw, rc, buf)
	written := lw.written

	// 明确关闭上游流：即使因超限提前中止，也要让引擎侧知道
	// "我们不要了"并释放它占用的连接与临时文件。
	// 单靠 defer 不够——这里要保证**在删文件之前**流已关闭。
	_ = rc.Close()

	syncErr := f.Sync()
	closeErr := f.Close()
	t.ioMu.Unlock()

	if werr != nil || syncErr != nil || closeErr != nil {
		// 配额类错误要给出可操作说明，不能笼统说"写失败"。
		switch {
		case errors.Is(werr, ErrQuotaExceeded):
			fail("传输配额不足，已中止并清理（请先释放其它传输任务）")
			return
		case errors.Is(werr, ErrArchiveTooLarge):
			fail("归档超过单次传输上限，已中止并清理")
			return
		case errors.Is(werr, context.Canceled), errors.Is(werr, context.DeadlineExceeded):
			fail(saveFailureMessage(runCtx, taskCtx, written))
			return
		}
		fail(saveFailureMessage(runCtx, taskCtx, written))
		return
	}
	// 硬限额复核：声明大小来自引擎，实际落盘量可能不同。
	// 这是**真正的**防线——上面的预检只是提前给出友好提示。
	if written > m.limits.MaxArchiveBytes {
		fail("归档实际大小超过单次传输上限，已删除")
		return
	}

	m.mu.Lock()
	t.updatedAt = m.now()
	if errors.Is(taskCtx.Err(), context.Canceled) {
		t.Status = StatusCanceled
		t.Message = "导出已取消"
		t.gone = true
		m.mu.Unlock()
		_ = m.removeArchiveWhenIdle(t)
		return
	}
	t.Status = StatusSucceeded
	t.Phase = "done"
	t.BytesDone = written
	t.BytesTotal = written
	t.SizeBytes = written
	t.SHA256 = hex.EncodeToString(h.Sum(nil))
	t.Message = "归档已就绪，可开始拉取"
	m.mu.Unlock()
}

// saveFailureMessage 区分"取消/超时"与"真的写不进去"。
//
// 不区分会误导：用户点了取消，界面却提示"磁盘空间不足"，
// 下次他就会去清磁盘——而问题根本不在那里。
func saveFailureMessage(runCtx, taskCtx context.Context, written int64) string {
	if errors.Is(taskCtx.Err(), context.Canceled) {
		return "导出已取消"
	}
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return "导出超时（超过 30 分钟）"
	}
	if written >= DefaultMaxArchiveBytes {
		return "归档超过单次传输上限，已中止"
	}
	return "写出归档失败（可能是磁盘空间不足或权限问题）"
}

// GetChunk 读取归档的一块，返回 base64 数据、回显 offset、是否到末尾、归档总大小。
//
// ============================ 与 release 的竞态 ============================
//
// 读之前必须登记（beginRead）并检查 gone：否则 release 可能已经把任务
// 移出表并开始删文件，而这边还拿着一个"已经 unlink 但还能读"的句柄，
// 读到的数据看起来正常，却已经不在磁盘上——下一次重试就 404，
// 数据流断裂的位置完全不可预测。
func (m *Manager) GetChunk(ctx context.Context, taskID string, offset, maxBytes int64) (string, int64, bool, int64, error) {
	if offset < 0 {
		return "", 0, false, 0, ErrBadOffset
	}

	t, ok := m.beginRead(taskID)
	if !ok {
		return "", 0, false, 0, ErrTaskNotFound
	}
	defer m.endRead(t)

	// 只有导出成功的任务才有可读归档。
	m.mu.Lock()
	st := t.Status
	m.mu.Unlock()
	if st != StatusSucceeded {
		return "", 0, false, 0, ErrTaskState
	}

	if maxBytes <= 0 {
		maxBytes = m.limits.ChunkSize
	}
	if maxBytes > m.limits.ChunkSize {
		// 超过上限按上限截断而不是报错：让 Hub 用固定块大小反复调用
		// 是最简单的用法，不该因为"要多了"就失败。
		maxBytes = m.limits.ChunkSize
	}

	f, size, err := m.store.openArchive(taskID)
	if err != nil {
		return "", 0, false, 0, err
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
