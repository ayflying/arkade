package agentlink

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"arkade/protocol/envelope"
	"github.com/gorilla/websocket"
)

type localFunc func(context.Context, string, []byte) ([]byte, *envelope.Error)

func (f localFunc) Call(ctx context.Context, m string, p []byte) ([]byte, *envelope.Error) {
	return f(ctx, m, p)
}
func register(t *testing.T, r *Registry, f localFunc) {
	t.Helper()
	if err := r.RegisterLocal("local", f); err != nil {
		t.Fatal(err)
	}
}
func TestLocalCallAndPresence(t *testing.T) {
	r := NewRegistry()
	register(t, r, func(ctx context.Context, m string, p []byte) ([]byte, *envelope.Error) {
		if m != envelope.MethodContainerList || string(p) != "{\"all\":true}" {
			t.Errorf("request: %s %s", m, p)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > requestTimeoutFor(m) {
			t.Error("method timeout missing")
		}
		return []byte(`{"ok":true}`), nil
	})
	if !r.IsLocal("local") || !r.IsReservedLocal("local") || !r.IsOnline("local") || r.Count() != 1 || r.Status("local", "offline") != "online" {
		t.Fatal("local not online")
	}
	if _, ok := r.Get("local"); ok {
		t.Fatal("Get fabricated remote connection")
	}
	if _, ok := r.OnlineIDs()["local"]; !ok {
		t.Fatal("missing online ID")
	}
	raw, err := r.Call(context.Background(), "local", envelope.MethodContainerList, map[string]bool{"all": true})
	if err != nil || string(raw) != `{"ok":true}` {
		t.Fatal(string(raw), err)
	}
	_, err = r.Call(context.Background(), "local", "bad", make(chan int))
	if err == nil || err.Code != envelope.CodeBadRequest {
		t.Fatal(err)
	}
	r.RemoveLocal("local")
	if r.IsOnline("local") || r.IsLocal("local") || !r.IsReservedLocal("local") || r.Count() != 0 {
		t.Fatal("remove released reservation")
	}
	_, err = r.Call(context.Background(), "local", "test", nil)
	if err == nil || err.Code != envelope.CodeAgentOffline {
		t.Fatal(err)
	}
}
func TestLocalProtocolErrorAndPanic(t *testing.T) {
	r := NewRegistry()
	register(t, r, func(context.Context, string, []byte) ([]byte, *envelope.Error) {
		return nil, &envelope.Error{Code: "denied", Message: "拒绝"}
	})
	_, err := r.Call(context.Background(), "local", "x", nil)
	if err == nil || err.Code != "denied" || err.Message != "拒绝" {
		t.Fatal(err)
	}
	r.RemoveLocal("local")
	register(t, r, func(context.Context, string, []byte) ([]byte, *envelope.Error) { panic("secret") })
	_, err = r.Call(context.Background(), "local", "x", nil)
	if err == nil || err.Code != "internal_error" || strings.Contains(err.Error(), "secret") {
		t.Fatal(err)
	}
}
func TestLocalTimeoutCancellationAndRemoval(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "remove", "close"} {
		t.Run(mode, func(t *testing.T) {
			r := NewRegistry()
			started := make(chan struct{})
			finished := make(chan struct{})
			register(t, r, func(ctx context.Context, _ string, _ []byte) ([]byte, *envelope.Error) {
				close(started)
				<-ctx.Done()
				close(finished)
				return nil, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			done := make(chan *ConnError, 1)
			go func() { _, err := r.Call(ctx, "local", "x", nil); done <- err }()
			<-started
			switch mode {
			case "cancel":
				cancel()
			case "remove":
				r.RemoveLocal("local")
			case "close":
				r.Close("local", "关闭")
			}
			err := <-done
			if err == nil {
				t.Fatal("no error")
			}
			if mode == "remove" || mode == "close" {
				if err.Code != envelope.CodeAgentOffline {
					t.Fatal(err)
				}
			} else if err.Code != "timeout" {
				t.Fatal(err)
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("executor context not canceled")
			}
		})
	}
}
func TestLocalMethodTimeoutPolicy(t *testing.T) {
	r := NewRegistry()
	register(t, r, func(ctx context.Context, m string, _ []byte) ([]byte, *envelope.Error) {
		deadline, _ := ctx.Deadline()
		remaining := time.Until(deadline)
		if remaining < requestTimeoutFor(m)-time.Second || remaining > requestTimeoutFor(m) {
			t.Errorf("method deadline %s: %v", m, remaining)
		}
		return nil, nil
	})
	for _, m := range []string{envelope.MethodComposeUp, envelope.MethodContainerList} {
		if _, err := r.Call(context.Background(), "local", m, nil); err != nil {
			t.Fatal(err)
		}
	}
}
func TestLocalRegistrationValidationAndConcurrentUse(t *testing.T) {
	r := NewRegistry()
	if r.ReserveLocalID("") == nil {
		t.Fatal("empty accepted")
	}
	var nilFunc localFunc
	if r.RegisterLocal("nil", nilFunc) == nil {
		t.Fatal("typed nil accepted")
	}
	f := localFunc(func(context.Context, string, []byte) ([]byte, *envelope.Error) { return []byte("ok"), nil })
	register(t, r, f)
	if r.RegisterLocal("local", f) == nil {
		t.Fatal("duplicate accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 10; n++ {
				r.Count()
				r.OnlineIDs()
				r.IsLocal("local")
				r.IsReservedLocal("local")
				_, _ = r.Call(context.Background(), "local", "x", nil)
			}
		}()
	}
	wg.Wait()
	r.RemoveLocal("local")
	register(t, r, f)
}

// 使用真实 WebSocket 对测试连接，保证远程分流和重复连接关闭逻辑没有回归。
func remoteConnection(t *testing.T, id string) *connection {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
		if err == nil {
			accepted <- ws
		}
	}))
	t.Cleanup(server.Close)
	client, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	c := newConnection(id, <-accepted, slog.Default())
	t.Cleanup(c.close)
	return c
}
func TestLocalReservationRejectsRemoteAndRemoteConflict(t *testing.T) {
	r := NewRegistry()
	if err := r.ReserveLocalID("reserved"); err != nil {
		t.Fatal(err)
	}
	c := remoteConnection(t, "reserved")
	r.Add(c)
	select {
	case <-c.closeCh:
	default:
		t.Fatal("reserved remote not closed")
	}
	if r.IsOnline("reserved") {
		t.Fatal("reserved only ID online")
	}
	c2 := remoteConnection(t, "remote")
	r.Add(c2)
	if r.ReserveLocalID("remote") == nil || r.RegisterLocal("remote", localFunc(func(context.Context, string, []byte) ([]byte, *envelope.Error) { return nil, nil })) == nil {
		t.Fatal("remote silently replaced")
	}
	r.Remove(c2)
	if err := r.ReserveLocalID("remote"); err != nil {
		t.Fatal(err)
	}
}
func TestLocalUncooperativeExecutorRemainsBounded(t *testing.T) {
	r := NewRegistry()
	release := make(chan struct{})
	var workers sync.WaitGroup
	started := make(chan struct{})
	register(t, r, func(context.Context, string, []byte) ([]byte, *envelope.Error) {
		defer workers.Done()
		started <- struct{}{}
		<-release
		return nil, nil
	})
	for i := 0; i < 64; i++ {
		workers.Add(1)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan *ConnError, 1)
		go func() { _, err := r.Call(ctx, "local", "x", nil); done <- err }()
		<-started
		cancel()
		err := <-done
		if err == nil || err.Code != "timeout" {
			t.Fatal(err)
		}
	}
	// 第65个调用只能等待槽位，不得再启动无视取消的执行器。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	_, err := r.Call(ctx, "local", "x", nil)
	cancel()
	if err == nil || err.Code != "timeout" || len(r.localSlots) != 64 {
		t.Fatal("unbounded local goroutines", err, len(r.localSlots))
	}
	close(release)
	workers.Wait()
}
func TestLocalConcurrentRemoveAndReregister(t *testing.T) {
	r := NewRegistry()
	f := localFunc(func(context.Context, string, []byte) ([]byte, *envelope.Error) { return []byte("ok"), nil })
	register(t, r, f)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				_, err := r.Call(context.Background(), "local", "x", nil)
				if err != nil && err.Code != envelope.CodeAgentOffline {
					t.Errorf("unexpected error: %v", err)
				}
				r.OnlineIDs()
			}
		}()
	}
	for i := 0; i < 100; i++ {
		r.RemoveLocal("local")
		if err := r.RegisterLocal("local", f); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if !r.IsOnline("local") || !r.IsReservedLocal("local") {
		t.Fatal("lost local registration")
	}
}

func TestRemoteRegistryCallRegression(t *testing.T) {
	r := NewRegistry()
	c := remoteConnection(t, "remote")
	r.Add(c)
	if got, ok := r.Get("remote"); !ok || got != c || r.Count() != 1 {
		t.Fatal("remote presence")
	}
	go func() {
		raw := <-c.enqueue
		var env envelope.Envelope
		_ = json.Unmarshal(raw, &env)
		c.mu.Lock()
		p := c.pending[env.ID]
		c.mu.Unlock()
		p.ch <- result{payload: []byte(`{"remote":true}`)}
	}()
	raw, err := r.Call(context.Background(), "remote", "test", nil)
	if err != nil || string(raw) != `{"remote":true}` {
		t.Fatal(string(raw), err)
	}
	replacement := remoteConnection(t, "remote")
	r.Add(replacement)
	r.Remove(c)
	if got, ok := r.Get("remote"); !ok || got != replacement {
		t.Fatal("old remove dropped replacement")
	}
	select {
	case <-c.closeCh:
	default:
		t.Fatal("old remote not closed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = r.Call(ctx, "remote", "test", nil)
	if err == nil || err.Code != "timeout" {
		t.Fatal(err)
	}
	r.Remove(replacement)
	_, err = r.Call(context.Background(), "remote", "test", nil)
	if err == nil || !errors.Is(err, ErrAgentOffline) {
		t.Fatal(err)
	}
}
