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
	// 上限就是 30 天：读到的合法最大值必须按 30 天算截止时间。
	f := &fakeStore{days: 30, cuts: make(chan time.Time, 10)}
	w := start(t.Context(), f, nil, 10*time.Millisecond, func() time.Time { return now })
	defer w.Close(context.Background())
	select {
	case cutoff := <-f.cuts:
		if !cutoff.Equal(now.AddDate(0, 0, -30)) {
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
	// 0 天（非法下限）与 31 天（超出 30 天上限）都必须拒绝清理：
	// 宁可多留一天，也不能按一个讲不通的天数去删历史。
	for _, days := range []int64{0, -1, 31, 90, 3650} {
		f := &fakeStore{days: days, cuts: make(chan time.Time, 1)}
		w := start(t.Context(), f, nil, time.Hour, time.Now)
		time.Sleep(10 * time.Millisecond)
		if err := w.Close(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(f.cuts) != 0 {
			t.Fatalf("非法保留期 %d 天仍然执行了清理", days)
		}
	}
}
func TestFailureRetriesNextPeriod(t *testing.T) {
	f := &fakeStore{days: 30, failures: 1, cuts: make(chan time.Time, 1)}
	w := start(t.Context(), f, nil, 5*time.Millisecond, time.Now)
	defer w.Close(context.Background())
	select {
	case <-f.cuts:
	case <-time.After(time.Second):
		t.Fatal("worker did not retry failed policy lookup")
	}
}

func TestCloseCanRetry(t *testing.T) {
	// 必须用**合法**天数：非法值现在会被直接跳过（不计入 f.cuts），
	// 用它做 fixture 会让 <-f.cuts 永远等不到，测试变成死锁而不是验证 Close。
	f := &fakeStore{days: 30, cuts: make(chan time.Time, 1), block: make(chan struct{})}
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
