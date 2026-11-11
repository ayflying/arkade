package stackmigrate

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type lifecycleBackend struct {
	*fakeBackend
	entered chan context.Context
	release chan struct{}
}

func (b *lifecycleBackend) CopyImage(ctx context.Context, source, target, ref string, p func(int64, int64)) error {
	select {
	case b.entered <- ctx:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-b.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func lifecycle(t *testing.T) (*Manager, *lifecycleBackend) {
	t.Helper()
	b := &lifecycleBackend{fakeBackend: newFakeBackend(), entered: make(chan context.Context, 64), release: make(chan struct{})}
	m := New(b)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m, b
}
func entered(t *testing.T, b *lifecycleBackend) context.Context {
	t.Helper()
	select {
	case ctx := <-b.entered:
		return ctx
	case <-time.After(time.Second):
		t.Fatal("worker did not enter copy")
		return nil
	}
}

func TestRequestCancellationPreservesValuesAndOwner(t *testing.T) {
	m, b := lifecycle(t)
	type identity struct{}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), identity{}, "actor-value"))
	o := baseOpts()
	o.OwnerID = "owner"
	s, e := m.Submit(WithOwner(ctx, "forged-owner"), o)
	if e != nil {
		t.Fatal(e)
	}
	worker := entered(t, b)
	cancel()
	if worker.Err() != nil || worker.Value(identity{}) != "actor-value" || OwnerIDFromContext(worker) != "owner" {
		t.Fatalf("worker identity/cancel incorrect %v %v", worker.Err(), OwnerIDFromContext(worker))
	}
	close(b.release)
	snap := waitTask(t, m, s.TaskID)
	if snap.Status != StatusSucceeded || snap.OwnerID != "owner" {
		t.Fatal(snap)
	}
}
func TestConcurrentSameOwnerRequestIsAtomic(t *testing.T) {
	m, b := lifecycle(t)
	const count = 32
	var wg sync.WaitGroup
	ids := make(chan string, count)
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o := baseOpts()
			o.OwnerID = "one"
			s, e := m.Submit(context.Background(), o)
			ids <- s.TaskID
			errs <- e
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	for got := range ids {
		if id == "" {
			id = got
		}
		if id != got {
			t.Fatal("request registered multiple tasks")
		}
	}
	entered(t, b)
	other := baseOpts()
	other.OwnerID = "two"
	if _, e := m.Submit(context.Background(), other); !errors.Is(e, ErrPairBusy) {
		t.Fatalf("owner reused other owner's task: %v", e)
	}
	close(b.release)
	waitTask(t, m, id)
	second, e := m.Submit(context.Background(), other)
	if e != nil || second.TaskID == id {
		t.Fatalf("owner isolation %v", e)
	}
	waitTask(t, m, second.TaskID)
}
func TestCloseCancelsWaitsAndRejectsSubmits(t *testing.T) {
	m, b := lifecycle(t)
	s, e := m.Submit(context.Background(), baseOpts())
	if e != nil {
		t.Fatal(e)
	}
	entered(t, b)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if e := m.Close(ctx); e != nil {
		t.Fatal(e)
	}
	snap, e := m.Get(s.TaskID, 0)
	if e != nil || snap.Status != StatusFailed || !snap.Residual.Uncertain {
		t.Fatalf("shutdown result %+v %v", snap, e)
	}
	if _, e := m.Submit(context.Background(), baseOpts()); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
	if e := m.Close(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestReversePairAndMaxTasksBound(t *testing.T) {
	m, b := lifecycle(t)
	_, e := m.Submit(context.Background(), baseOpts())
	if e != nil {
		t.Fatal(e)
	}
	entered(t, b)
	reverse := baseOpts()
	reverse.Source, reverse.Target = "b", "a"
	reverse.RequestID = "reverse"
	if _, e := m.Submit(context.Background(), reverse); !errors.Is(e, ErrPairBusy) {
		t.Fatal(e)
	}
	for i := 1; i < MaxTasks; i++ {
		source, target := fmt.Sprintf("s%d", i), fmt.Sprintf("t%d", i)
		b.fakeBackend.mu.Lock()
		b.online[source] = true
		b.online[target] = true
		b.manage[source+"/demo"] = true
		b.manage[target+"/demo"] = true
		b.fakeBackend.mu.Unlock()
		_, e := m.Submit(context.Background(), Options{Source: source, Target: target, StackName: "demo"})
		if e != nil {
			t.Fatal(e)
		}
		entered(t, b)
	}
	if _, e := m.Submit(context.Background(), Options{Source: "extra", Target: "more", StackName: "demo"}); !errors.Is(e, ErrTooManyTasks) {
		t.Fatal(e)
	}
}
func TestCloseDeadlineCanBeRetried(t *testing.T) {
	b := &blockingBackend{fakeBackend: newFakeBackend(), entered: make(chan struct{}, 1), release: make(chan struct{})}
	m := New(b)
	s, e := m.Submit(context.Background(), baseOpts())
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-b.entered:
	case <-time.After(time.Second):
		t.Fatal("worker missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if e := m.Close(ctx); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatalf("close should honor deadline: %v", e)
	}
	close(b.release)
	if e := m.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	final, _ := m.Get(s.TaskID, 0)
	if !terminal(final.Status) {
		t.Fatal(final)
	}
}

func TestTerminalRetentionIsBounded(t *testing.T) {
	m := New(newFakeBackend())
	defer m.Close(context.Background())
	for i := 0; i < MaxTasks*3; i++ {
		o := baseOpts()
		o.RequestID = fmt.Sprintf("req-%d", i)
		s, e := m.Submit(context.Background(), o)
		if e != nil {
			t.Fatal(e)
		}
		waitTask(t, m, s.TaskID)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.tasks) > MaxTasks || len(m.byRequest) > MaxTasks {
		t.Fatalf("unbounded tasks %d requests %d", len(m.tasks), len(m.byRequest))
	}
}
