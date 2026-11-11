package migrate

import (
	"context"
	"encoding/json"
	"errors"
	"os"
)

var ErrTargetVolumeExists = errors.New("目标数据卷已存在，已拒绝覆盖")

const importOwnerLabel = "io.arkade.migration.owner"

var volumeRestoreSem = make(chan struct{}, 1)

// Docker create is idempotent but never replaces an existing volume's labels.
// A fresh unguessable owner proves this task created the empty destination.
func (m *Importer) reserveVolume(ctx context.Context, volume string) error {
	owner := newExportID()
	if _, err := m.docker(ctx, "volume", "create", "--label", importOwnerLabel+"="+owner, volume); err != nil {
		return errors.New("无法创建目标数据卷，已拒绝导入")
	}
	out, err := m.docker(ctx, "volume", "inspect", volume)
	if err != nil {
		return errors.New("无法确认目标数据卷归属，已拒绝导入")
	}
	var rows []struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
	}
	if json.Unmarshal(out, &rows) != nil || len(rows) != 1 || rows[0].Name != volume || rows[0].Labels[importOwnerLabel] != owner {
		return ErrTargetVolumeExists
	}
	return nil
}

// Cancel interrupts extraction and cleans only this task's temporary archive.
// A partially restored target volume is retained for diagnosis, never deleted.
func (m *Importer) Cancel(ctx context.Context, id string) (ImportSnapshot, error) {
	m.mu.Lock()
	t, ok := m.task[id]
	m.mu.Unlock()
	if !ok {
		return ImportSnapshot{}, ErrImportNotFound
	}
	t.mu.Lock()
	if importTerminal(t.status) {
		failed := t.cleanupFailed
		t.mu.Unlock()
		if failed {
			return t.snapshot(), ErrHelperCleanup
		}
		return t.snapshot(), nil
	}
	cancel := t.cancel
	t.status = ImportCanceled
	t.phase = "已取消"
	t.message = "导入已取消；已写入的目标卷数据保留，不自动删除"
	t.finishedAt = m.now()
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	t.ioMu.Lock()
	defer t.ioMu.Unlock()
	t.mu.Lock()
	failed := t.cleanupFailed
	t.mu.Unlock()
	if failed {
		return t.snapshot(), ErrHelperCleanup
	}
	if err := os.RemoveAll(t.dir); err != nil {
		return t.snapshot(), err
	}
	return t.snapshot(), nil
}
