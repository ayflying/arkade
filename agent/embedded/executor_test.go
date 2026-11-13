package embedded

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

func TestNoneRuntimeStillProvidesLocalHandlers(t *testing.T) {
	initCtx, cancel := context.WithCancel(context.Background())
	e, err := New(initCtx, Options{DataDir: t.TempDir(), Runtime: "none"})
	if err != nil {
		t.Fatal(err)
	}
	cancel() // 构造请求结束不应关闭执行器。
	defer e.Close(context.Background())
	info, err := e.Info(context.Background())
	if err != nil || info.Kind != "none" || len(info.Capabilities) == 0 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	_, rpcErr := e.Call(context.Background(), "unknown.method", []byte(`{}`))
	if rpcErr == nil || rpcErr.Code != envelope.CodeUnsupported {
		t.Fatalf("wrong unknown method error: %v", rpcErr)
	}
	raw, rpcErr := e.Call(context.Background(), envelope.MethodContainerList, []byte(`{}`))
	if rpcErr != nil {
		t.Fatalf("none runtime should preserve empty-list handler behavior: %v", rpcErr)
	}
	var list envelope.ContainerListResult
	if err := json.Unmarshal(raw, &list); err != nil || len(list.Containers) != 0 || list.AgentID != "agent-local" {
		t.Fatalf("unexpected none runtime list: %s", raw)
	}
	_, rpcErr = e.Call(context.Background(), envelope.MethodRuntimeInfo, []byte(`{broken`))
	if rpcErr == nil || rpcErr.Code != envelope.CodeBadRequest {
		t.Fatalf("invalid JSON error=%v", rpcErr)
	}
}

func TestMissingRuntimeDegradesWithoutBlockingStartup(t *testing.T) {
	e, err := New(context.Background(), Options{DataDir: t.TempDir(), Endpoint: "unix:///nonexistent/arkade-embedded-test.sock"})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(context.Background())
	info, err := e.Info(context.Background())
	if err != nil || info.Kind != "none" {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}

func TestWorkspaceInitializationErrorsAreNotSilent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-directory")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(context.Background(), Options{DataDir: file, Runtime: "none"}); err == nil {
		t.Fatal("workspace error silently ignored")
	}
	if _, err := New(context.Background(), Options{DataDir: t.TempDir(), Runtime: "invalid"}); err == nil {
		t.Fatal("unknown runtime silently accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(ctx, Options{DataDir: t.TempDir(), Runtime: "none"}); err == nil {
		t.Fatal("canceled initialization accepted")
	}
}

func dockerTestServer(t *testing.T, containers func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			w.Write([]byte(`{"Version":"27.1","ApiVersion":"1.46","Components":[{"Name":"Engine","Version":"27.1"}]}`))
		case "/containers/json":
			containers(w, r)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.Close)
	return s
}
func TestCallUsesHandlersAndStampsAgentIdentity(t *testing.T) {
	s := dockerTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("unexpected mutation")
		}
		w.Write([]byte(`[{"Id":"full-container-id","Names":["/web"],"Image":"nginx","State":"running","Env":["PASSWORD=secret"]}]`))
	})
	for _, id := range []string{"", "custom-local"} {
		e, err := New(context.Background(), Options{DataDir: t.TempDir(), Endpoint: s.URL, AgentID: id})
		if err != nil {
			t.Fatal(err)
		}
		info, err := e.Info(context.Background())
		if err != nil || info.Kind != "docker" || info.Version != "27.1" {
			t.Fatalf("info=%+v err=%v", info, err)
		}
		raw, rpcErr := e.Call(context.Background(), envelope.MethodContainerList, []byte(`{"all":true}`))
		if rpcErr != nil {
			t.Fatal(rpcErr)
		}
		if strings.Contains(string(raw), "PASSWORD") || strings.Contains(string(raw), "secret") {
			t.Fatal("raw runtime secrets leaked")
		}
		var list envelope.ContainerListResult
		if err := json.Unmarshal(raw, &list); err != nil {
			t.Fatal(err)
		}
		want := id
		if want == "" {
			want = "agent-local"
		}
		if list.AgentID != want {
			t.Fatalf("agent_id=%q want=%q", list.AgentID, want)
		}
		e.Close(context.Background())
	}
}

func TestCloseCancelsInflightCallAndIsIdempotent(t *testing.T) {
	started := make(chan struct{})
	s := dockerTestServer(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	e, err := New(context.Background(), Options{DataDir: t.TempDir(), Endpoint: s.URL})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, rpcErr := e.Call(context.Background(), envelope.MethodContainerList, []byte(`{}`))
		if rpcErr == nil {
			t.Error("inflight canceled request returned success")
		}
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := e.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("call did not exit")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.Close(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if _, err := e.Info(context.Background()); err == nil {
		t.Fatal("closed executor info succeeded")
	}
	if _, err := e.Call(context.Background(), envelope.MethodRuntimeInfo, nil); err == nil || err.Code != envelope.CodeAgentOffline {
		t.Fatalf("closed executor call=%v", err)
	}
	select {
	case <-e.sweepDone:
	default:
		t.Fatal("sweeper leaked")
	}
}

// ctx 只限制调用者等待，超时后清理仍必须继续，后续 Close 可重新等待。
func TestCloseTimeoutContinuesCleanup(t *testing.T) {
	e, err := New(context.Background(), Options{DataDir: t.TempDir(), Runtime: "none"})
	if err != nil {
		t.Fatal(err)
	}
	cleanup := make(chan struct{})
	entered := make(chan struct{})
	e.reg.AddCloser(func() { close(entered); <-cleanup })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := e.Close(ctx); err != context.DeadlineExceeded {
		t.Fatalf("expected bounded wait: %v", err)
	}
	<-entered
	select {
	case <-e.closeDone:
		t.Fatal("cleanup falsely reported done")
	default:
	}
	close(cleanup)
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := e.Close(waitCtx); err != nil {
		t.Fatalf("background cleanup did not finish: %v", err)
	}
}

func TestHelperLeavesNormalHubArgumentsUntouched(t *testing.T) {
	for _, args := range [][]string{nil, {"serve"}, {"volume-file-helper-unknown"}} {
		if handled, _ := RunHelper(args); handled {
			t.Fatalf("normal args handled: %v", args)
		}
	}
}
