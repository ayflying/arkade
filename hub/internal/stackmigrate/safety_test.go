package stackmigrate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type changingBackend struct {
	*fakeBackend
	change string
}

func (b *changingBackend) CopyImage(ctx context.Context, s, d, ref string, p func(int64, int64)) error {
	if e := b.fakeBackend.CopyImage(ctx, s, d, ref, p); e != nil {
		return e
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.change {
	case "source":
		b.config += "\n#changed"
	case "permission":
		b.manage["b/demo"] = false
	case "target":
		b.exists["b/demo"] = true
	}
	return nil
}
func TestStageGuardsDetectMidTransferChanges(t *testing.T) {
	for _, change := range []string{"source", "permission", "target"} {
		t.Run(change, func(t *testing.T) {
			b := &changingBackend{fakeBackend: newFakeBackend(), change: change}
			m := New(b)
			defer m.Close(context.Background())
			s, e := m.Submit(context.Background(), baseOpts())
			if e != nil {
				t.Fatal(e)
			}
			final := waitTask(t, m, s.TaskID)
			if final.Status != StatusFailed || final.Residual.ImagesImported != 1 || b.importCalls != 0 {
				t.Fatalf("guard missed mutation %+v imports=%d", final, b.importCalls)
			}
		})
	}
}

type partialBackend struct{ *fakeBackend }

func (b *partialBackend) ImportCompose(context.Context, string, string, string, string, string) error {
	return &ResidualError{Err: errors.New("DB save failed after target commit"), Residual: Residual{ComposeWritten: true}}
}
func TestPartialEffectsAreNotReportedAsRollback(t *testing.T) {
	m := New(&partialBackend{newFakeBackend()})
	defer m.Close(context.Background())
	s, e := m.Submit(context.Background(), baseOpts())
	if e != nil {
		t.Fatal(e)
	}
	final := waitTask(t, m, s.TaskID)
	if !final.Residual.ComposeWritten || strings.Contains(final.Error, "未留下任何改动") {
		t.Fatal(final)
	}
}

type concurrentProgressBackend struct{ *fakeBackend }

func (b *concurrentProgressBackend) CopyImage(ctx context.Context, s, d, ref string, p func(int64, int64)) error {
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := int64(1); n <= 100; n++ {
				p(n, 100)
			}
		}()
	}
	wg.Wait()
	return nil
}
func TestConcurrentProgressIsBoundedAndSnapshotsIndependent(t *testing.T) {
	m := New(&concurrentProgressBackend{newFakeBackend()})
	defer m.Close(context.Background())
	s, e := m.Submit(context.Background(), baseOpts())
	if e != nil {
		t.Fatal(e)
	}
	final := waitTask(t, m, s.TaskID)
	if len(final.Events) > 40 {
		t.Fatal(len(final.Events))
	}
	final.Events[0].Text = "tampered"
	again, e := m.Get(s.TaskID, 0)
	if e != nil || again.Events[0].Text == "tampered" {
		t.Fatal("snapshot aliases mutable event buffer")
	}
}

type panicBackend struct{ *fakeBackend }

func (*panicBackend) CopyImage(context.Context, string, string, string, func(int64, int64)) error {
	panic("backend panic")
}
func TestEventByteLimitIncludesSingleOversizedEvent(t *testing.T) {
	task := &Task{}
	task.appendEvent(StreamStdout, strings.Repeat("界", maxEventBytes))
	snap := task.snapshot(0)
	if !snap.Truncated || len(snap.Events) != 1 || len(snap.Events[0].Text) > maxEventBytes {
		t.Fatal("oversized event escaped memory bound")
	}
}

func TestPanicFinalizesAndReleasesPair(t *testing.T) {
	m := New(&panicBackend{newFakeBackend()})
	defer m.Close(context.Background())
	s, e := m.Submit(context.Background(), baseOpts())
	if e != nil {
		t.Fatal(e)
	}
	final := waitTask(t, m, s.TaskID)
	if final.Status != StatusFailed || !final.Residual.Uncertain {
		t.Fatal(final)
	}
	o := baseOpts()
	o.RequestID = "retry"
	if _, e := m.Submit(context.Background(), o); e != nil {
		t.Fatal(e)
	}
}
