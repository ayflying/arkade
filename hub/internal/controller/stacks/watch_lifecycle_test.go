package stacks

import (
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatcherCloseCancelsActivePollAndRejectsNew(t *testing.T) {
	var m operationWatchers
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	var calls atomic.Int32
	poll := func(ctx context.Context, _ string, _ envelope.ComposeOperationGetPayload) (envelope.ComposeOperationResult, *agentlink.ConnError) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(cancelled)
		return envelope.ComposeOperationResult{}, &agentlink.ConnError{Code: "timeout"}
	}
	op := store.ComposeOperation{AgentID: "a", TaskID: "task", Name: "app", Action: "up"}
	m.start(op, time.Millisecond, poll, nil)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("poll not started")
	}
	if err := m.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("Close returned before poll exited")
	}
	op.TaskID = "new"
	m.start(op, time.Millisecond, poll, nil)
	m.mu.Lock()
	active := len(m.active)
	m.mu.Unlock()
	if active != 0 || calls.Load() != 1 {
		t.Fatalf("closed watcher accepted work: %d %d", active, calls.Load())
	}
}
func TestWatcherCloseTimeoutRetryWaitsForBackend(t *testing.T) {
	var m operationWatchers
	entered := make(chan struct{})
	release := make(chan struct{})
	m.start(store.ComposeOperation{AgentID: "a", TaskID: "task"}, time.Millisecond, func(context.Context, string, envelope.ComposeOperationGetPayload) (envelope.ComposeOperationResult, *agentlink.ConnError) {
		close(entered)
		<-release
		return envelope.ComposeOperationResult{}, nil
	}, nil)
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := m.close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close did not wait: %v", err)
	}
	close(release)
	if err := m.close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestWatcherConcurrentCloseAndStart(t *testing.T) {
	var m operationWatchers
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.start(store.ComposeOperation{AgentID: "a", TaskID: "task"}, time.Millisecond, func(ctx context.Context, _ string, _ envelope.ComposeOperationGetPayload) (envelope.ComposeOperationResult, *agentlink.ConnError) {
				<-ctx.Done()
				return envelope.ComposeOperationResult{}, nil
			}, nil)
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = m.close(context.Background()) }()
	wg.Wait()
	if err := m.close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
