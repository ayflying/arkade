package embedded

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

// 当前测试二进制兼任 fake docker，跨平台捕获真实子进程 DOCKER_HOST。
func TestMain(m *testing.M) {
	// Runner 总是以 compose 为首参数调用；无论标记是否丢失，都禁止进入 m.Run。
	// 这是与环境标记独立的递归保险，效果等同于子测试精确 -run。
	if len(os.Args) > 1 && os.Args[1] == "compose" {
		if os.Getenv("TEST_EMBEDDED_DOCKER") != "1" {
			fmt.Fprint(os.Stderr, "fake docker marker missing")
			os.Exit(2)
		}
		if capture := os.Getenv("TEST_EMBEDDED_DOCKER_CAPTURE"); capture != "" {
			if err := os.WriteFile(capture, []byte(os.Getenv("DOCKER_HOST")+"\n"+os.Getenv("DOCKER_CONTEXT")), 0600); err != nil {
				os.Exit(3)
			}
		}
		fmt.Print("[]")
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestHTTPRuntimeAndComposeUseSameEndpoint(t *testing.T) {
	s := dockerTestServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) })
	t.Setenv("DOCKER_HOST", "tcp://wrong-daemon.invalid:2375")
	t.Setenv("DOCKER_CONTEXT", "wrong-inherited-context")
	t.Setenv("TEST_EMBEDDED_DOCKER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "fake-docker-host")
	t.Setenv("TEST_EMBEDDED_DOCKER_CAPTURE", capture)
	project := filepath.Join(dir, "stacks", "demo")
	if err := os.MkdirAll(project, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "compose.yml"), []byte("services:\n  web:\n    image: nginx\n"), 0644); err != nil {
		t.Fatal(err)
	}
	e, err := New(context.Background(), Options{DataDir: dir, Endpoint: s.URL, ComposeBin: exe})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(context.Background())
	// 先验证 HTTP 容器处理器使用显式远端，而非继承环境中的错误 daemon。
	if _, rpcErr := e.Call(context.Background(), envelope.MethodContainerList, []byte(`{}`)); rpcErr != nil {
		t.Fatal(rpcErr)
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, rpcErr := e.Call(callCtx, envelope.MethodComposeStop, []byte(`{"name":"demo"}`))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	output, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	want := "tcp://" + strings.TrimPrefix(s.URL, "http://") + "\n"
	if string(output) != want {
		t.Fatalf("compose daemon mismatch: output=%q want=%q", output, want)
	}
}
func TestEmbeddedInitializationCancelsStalledProbe(t *testing.T) {
	// 复用 HTTP 假服务端，但 /version 永远不返回头。
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := New(ctx, Options{DataDir: dir, Endpoint: server.URL}); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("want canceled got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("embedded New hung on probe")
	}
}
