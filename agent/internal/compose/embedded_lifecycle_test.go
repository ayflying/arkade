package compose

import (
	"context"
	"testing"
	"time"
)

// 不依赖 shell：模拟 worker 在收到取消后仍需异步收尾，Close 必须 join 而非只 cancel。
func TestPullCloseWaitsForWorkerCleanup(t *testing.T) {
	p := NewPullManager(nil, context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	cleanup := make(chan struct{})
	p.mu.Lock()
	p.byID["test"] = &PullTask{cancel: cancel}
	p.workers.Add(1)
	p.mu.Unlock()
	go func() { defer p.workers.Done(); <-ctx.Done(); <-cleanup }()
	closed := make(chan struct{})
	go func() { p.Close(); close(closed) }()
	<-ctx.Done()
	select {
	case <-closed:
		t.Fatal("Close returned before worker cleanup")
	default:
	}
	close(cleanup)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close failed to join worker")
	}
	if _, err := p.StartPull("demo", "closed-request"); err == nil {
		t.Fatal("closed manager accepted new task")
	}
	p.Close()
}
