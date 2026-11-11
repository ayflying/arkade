package migrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PutChunk 写入一块字节。
//
// offset 必须**正好等于已接收字节数**：允许任意 offset 会让"重传同一块"
// 变成静默的重复写入，最终文件长度正确但内容错乱，而校验和要到最后才报错。
// 只允许"从当前位置继续"这一个含义，语义最简单也最难出错。
func (m *Importer) PutChunk(ctx context.Context, taskID string, offset int64, data []byte) (int64, error) {
	if len(data) > ChunkSize {
		return 0, ErrSizeMismatch
	}
	if offset < 0 {
		return 0, ErrBadOffset
	}
	m.mu.Lock()
	t, ok := m.task[taskID]
	m.mu.Unlock()
	if !ok {
		return 0, ErrImportNotFound
	}

	t.ioMu.Lock()
	defer t.ioMu.Unlock()
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
		if int64(len(data)) > current-offset {
			return current, ErrBadOffset
		}
		f, err := os.Open(filepath.Join(t.dir, "archive.tar"))
		if err != nil {
			return current, err
		}
		defer f.Close()
		prior := make([]byte, len(data))
		if _, err := f.ReadAt(prior, offset); err != nil {
			return current, err
		}
		if !bytes.Equal(prior, data) {
			return current, ErrImportParamMismatch
		}
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

	t.ioMu.Lock()
	defer t.ioMu.Unlock()
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

	runCtx, cancel := context.WithTimeout(ctx, importTaskTimeout)
	t.mu.Lock()
	if t.status == ImportCanceled {
		t.mu.Unlock()
		cancel()
		return t.snapshot(), context.Canceled
	}
	t.cancel = cancel
	t.mu.Unlock()
	defer cancel()
	select {
	case volumeRestoreSem <- struct{}{}:
	case <-runCtx.Done():
		t.fail("等待卷恢复资源时请求已取消或超时，目标卷未被修改")
		return t.snapshot(), runCtx.Err()
	}
	defer func() { <-volumeRestoreSem }()
	if err := runCtx.Err(); err != nil {
		t.fail("等待卷恢复资源时请求已取消或超时，目标卷未被修改")
		return t.snapshot(), err
	}
	if err := m.reserveVolume(runCtx, t.volume); err != nil {
		t.fail("目标数据卷已存在或无法确认安全，已拒绝覆盖")
		return t.snapshot(), err
	}
	// 解包：临时容器把归档解进目标卷。
	// 目标卷不存在时 docker run -v <name>:/to 会自动创建它。
	out, derr := runArchiveHelper(runCtx, m.docker,
		"-v", t.volume+":/to",
		"-v", t.dir+":/from:ro",
		"alpine:3.20",
		"tar", "-xf", "/from/archive.tar", "-C", "/to")

	t.mu.Lock()
	t.finishedAt = m.now()
	if errors.Is(derr, ErrHelperCleanup) {
		t.cleanupFailed = true
		t.status = ImportFailed
		t.phase = "清理失败"
		t.message = ErrHelperCleanup.Error()
		t.errText = t.message
		t.mu.Unlock()
		return t.snapshot(), derr
	}
	if t.status == ImportCanceled {
		t.mu.Unlock()
		return t.snapshot(), context.Canceled
	}
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
