package auditretention

import (
	"arkade/hub/internal/store"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeStore struct {
	mu       sync.Mutex
	days     int64
	cuts     chan time.Time
	block    chan struct{}
	failures int
}

func (f *fakeStore) GetSystemSettings(ctx context.Context) (store.SystemSettings, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures > 0 {
		f.failures--
		return store.SystemSettings{}, errors.New("temporary database failure")
	}
	return store.SystemSettings{AuditRetentionDays: f.days}, nil
}
func (f *fakeStore) PruneAuditBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	f.cuts <- cutoff
	if f.block != nil {
		<-f.block
	}
	return 0, nil
}
func TestDynamicPolicyAndShutdown(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	f := &fakeStore{days: 3650, cuts: make(chan time.Time, 10)}
	w := start(t.Context(), f, nil, 10*time.Millisecond, func() time.Time { return now })
	defer w.Close(context.Background())
	select {
	case cutoff := <-f.cuts:
		if !cutoff.Equal(now.AddDate(0, 0, -3650)) {
			t.Fatal(cutoff)
		}
	case <-time.After(time.Second):
		t.Fatal("no startup sweep")
	}
	f.mu.Lock()
	f.days = 1
	f.mu.Unlock()
	select {
	case cutoff := <-f.cuts:
		if !cutoff.Equal(now.AddDate(0, 0, -1)) {
			t.Fatal(cutoff)
		}
	case <-time.After(time.Second):
		t.Fatal("no policy reload")
	}
	if err := w.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
func TestInvalidPolicySkipsDelete(t *testing.T) {
	f := &fakeStore{days: 0, cuts: make(chan time.Time, 1)}
	w := start(t.Context(), f, nil, time.Hour, time.Now)
	time.Sleep(10 * time.Millisecond)
	if err := w.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(f.cuts) != 0 {
		t.Fatal("invalid policy deleted records")
	}
}
func TestFailureRetriesNextPeriod(t *testing.T) {
	f := &fakeStore{days: 90, failures: 1, cuts: make(chan time.Time, 1)}
	w := start(t.Context(), f, nil, 5*time.Millisecond, time.Now)
	defer w.Close(context.Background())
	select {
	case <-f.cuts:
	case <-time.After(time.Second):
		t.Fatal("worker did not retry failed policy lookup")
	}
}

func TestCloseCanRetry(t *testing.T) {
	f := &fakeStore{days: 90, cuts: make(chan time.Time, 1), block: make(chan struct{})}
	w := start(t.Context(), f, nil, time.Hour, time.Now)
	<-f.cuts
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if w.Close(ctx) == nil {
		t.Fatal("close unexpectedly finished")
	}
	close(f.block)
	if err := w.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
