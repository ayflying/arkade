package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 已接受 TCP 但不返回 HTTP 头，DialTimeout 不会触发，必须靠整个探测 ctx。
func TestNewWithContextCancelsStalledProbe(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		rt, err := NewWithContext(ctx, Config{Socket: server.URL})
		if !IsNone(rt) {
			t.Error("canceled probe returned live runtime")
		}
		result <- err
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want canceled got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stalled probe ignored cancellation")
	}
}
func TestNewWithContextDeadlineBoundsProbe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewWithContext(ctx, Config{Socket: server.URL})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatalf("probe did not honor deadline: %v", err)
	}
}
