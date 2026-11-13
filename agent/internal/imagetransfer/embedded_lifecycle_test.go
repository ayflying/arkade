package imagetransfer

import (
	"testing"
	"time"

	"arkade/agent/internal/runtime"
)

// 已取消/终态不等于 writer 已退出。关闭必须等 done 再清理任务。
func TestCloseWaitsForTerminalWriter(t *testing.T) {
	m, err := NewManager(t.TempDir(), runtime.NewNone(""), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	task, err := m.acquireTaskLocked(OpSave, "test")
	if err != nil {
		t.Fatal(err)
	}
	task.Status = StatusCanceled
	m.mu.Unlock()
	closed := make(chan struct{})
	go func() { m.Close(); close(closed) }()
	select {
	case <-task.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel terminal writer")
	}
	select {
	case <-closed:
		t.Fatal("Close ignored live writer")
	default:
	}
	m.endWrite(task)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close failed to join writer")
	}
	m.mu.Lock()
	_, err = m.acquireTaskLocked(OpSave, "after-close")
	m.mu.Unlock()
	if err == nil {
		t.Fatal("closed manager accepted new writer")
	}
	m.Close()
}
