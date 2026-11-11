package migrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

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
		expired := !t.cleanupFailed && importTerminal(t.status) && now.Sub(t.finishedAt) > VolumeImportTTL
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
