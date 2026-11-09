package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeEngine 是一个可编程的假 Docker/Podman 服务端。
//
// 为什么用 httptest 而不是真的 Docker：CI 与开发机上没有守护进程，
// 测试必须能脱离真实引擎运行；同时只有假服务端才能稳定地构造
// "/version 返回 Podman 组件"、"容器列表 404" 这类边界。
type fakeEngine struct {
	t *testing.T
	// kind 决定 /version 返回的组件（docker 返回 Engine，podman 返回 Podman Engine）。
	kind string
	// failStatus 非 0 时，所有容器端点都返回该状态码。
	failStatus int
	// logs 是 /logs 的响应正文。
	logs string
	// calls 记录被调用的路径，用于断言请求参数（如 tail 行数）。
	calls []string
}

// server 启动假服务端并返回运行时实现。
func (f *fakeEngine) server() (*httptest.Server, Runtime) {
	mux := http.NewServeMux()
	mux.HandleFunc(pathVersion, func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.Path)
		resp := map[string]any{
			"Version":    "27.3.1",
			"ApiVersion": "1.47",
			"Components": []map[string]any{},
		}
		if f.kind == string(KindPodman) {
			resp["Version"] = "5.2.0"
			resp["Components"] = []map[string]any{{"Name": "Podman Engine", "Version": "5.2.0"}}
		} else {
			resp["Components"] = []map[string]any{{"Name": "Engine", "Version": "27.3.1"}}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc(pathContainers, func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.RequestURI())
		if f.failStatus != 0 {
			w.WriteHeader(f.failStatus)
			return
		}
		// 故意返回两条记录：一条运行中带端口映射，一条已停止无端口。
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"Id":      "0123456789abcdef0123456789abcdef",
				"Names":   []string{"/web"},
				"Image":   "nginx:1.27",
				"Created": 1700000000,
				"State":   "running",
				"Status":  "Up 3 hours",
				"Ports": []map[string]any{
					{"IP": "0.0.0.0", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"},
				},
			},
			{
				"Id":      "fedcba9876543210fedcba9876543210",
				"Names":   []string{"/db"},
				"Image":   "postgres:16",
				"Created": 1700000100,
				"State":   "exited",
				"Status":  "Exited (0) 1 hour ago",
			},
		})
	})
	mux.HandleFunc(pathContainerPrefix+"web"+actionStart, func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(pathContainerPrefix+"web"+actionStop, func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.Path+"?"+r.URL.RawQuery)
		if f.failStatus == http.StatusNotFound {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(pathContainerPrefix+"web"+actionKill, func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc(pathContainerPrefix+"missing"+actionStart, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no such container"))
	})
	mux.HandleFunc(pathContainerPrefix+"web"+actionLogs, func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, r.URL.RequestURI())
		_, _ = w.Write([]byte(f.logs))
	})

	srv := httptest.NewServer(mux)
	rt, err := probe(context.Background(), "tcp://"+strings.TrimPrefix(srv.URL, "http://"), false)
	if err != nil {
		srv.Close()
		f.t.Fatalf("探测失败: %v", err)
	}
	return srv, rt
}

func TestProbeDetectsDocker(t *testing.T) {
	f := &fakeEngine{t: t, kind: "docker"}
	srv, rt := f.server()
	defer srv.Close()

	info, err := rt.Info(context.Background())
	if err != nil {
		t.Fatalf("Info 失败: %v", err)
	}
	if info.Kind != KindDocker {
		t.Fatalf("引擎类型 = %q，期望 docker", info.Kind)
	}
	if info.Version != "27.3.1" {
		t.Fatalf("版本 = %q，期望 27.3.1", info.Version)
	}
	if info.Rootless {
		t.Fatalf("Docker 不应被判定为 rootless")
	}
}

func TestProbeDetectsPodman(t *testing.T) {
	f := &fakeEngine{t: t, kind: "podman"}
	srv, rt := f.server()
	defer srv.Close()

	info, err := rt.Info(context.Background())
	if err != nil {
		t.Fatalf("Info 失败: %v", err)
	}
	if info.Kind != KindPodman {
		t.Fatalf("引擎类型 = %q，期望 podman", info.Kind)
	}
}

func TestList(t *testing.T) {
	f := &fakeEngine{t: t, kind: "docker"}
	srv, rt := f.server()
	defer srv.Close()

	list, err := rt.List(context.Background(), true)
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("容器数 = %d，期望 2", len(list))
	}
	if list[0].Name != "web" {
		t.Fatalf("容器名 = %q，期望去掉前导斜杠后的 web", list[0].Name)
	}
	if len(list[0].Ports) != 1 || list[0].Ports[0].Public != 8080 {
		t.Fatalf("端口解析错误: %+v", list[0].Ports)
	}
	// all=false 时请求参数必须是 all=0：这是协议里"只看运行中"的语义落点。
	if _, err := rt.List(context.Background(), false); err != nil {
		t.Fatalf("List(all=false) 失败: %v", err)
	}
	if !strings.Contains(f.calls[len(f.calls)-1], "all=0") {
		t.Fatalf("all=false 时未带 all=0 参数，实际请求: %s", f.calls[len(f.calls)-1])
	}
}

func TestListHTTPError(t *testing.T) {
	f := &fakeEngine{t: t, kind: "docker", failStatus: http.StatusInternalServerError}
	srv, rt := f.server()
	defer srv.Close()

	_, err := rt.List(context.Background(), true)
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	// 5xx 不应被误判成"容器不存在"：两者的处理路径不同。
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("500 不应映射为 ErrNotFound: %v", err)
	}
}

func TestStartNotFound(t *testing.T) {
	f := &fakeEngine{t: t, kind: "docker"}
	srv, rt := f.server()
	defer srv.Close()

	err := rt.Start(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("404 应映射为 ErrNotFound，实际: %v", err)
	}
}

func TestStartAndStop(t *testing.T) {
	f := &fakeEngine{t: t, kind: "docker"}
	srv, rt := f.server()
	defer srv.Close()

	ctx := context.Background()
	if err := rt.Start(ctx, "web"); err != nil {
		t.Fatalf("Start 失败: %v", err)
	}
	if err := rt.Stop(ctx, "web", false); err != nil {
		t.Fatalf("Stop 失败: %v", err)
	}
	last := f.calls[len(f.calls)-1]
	if !strings.Contains(last, actionStop) || !strings.Contains(last, "t=") {
		t.Fatalf("优雅停止应带超时参数，实际请求: %s", last)
	}

	// force=true 必须走 kill 而不是带超时的 stop，否则"卡死进程"停不掉。
	if err := rt.Stop(ctx, "web", true); err != nil {
		t.Fatalf("强制停止失败: %v", err)
	}
	if !strings.Contains(f.calls[len(f.calls)-1], actionKill) {
		t.Fatalf("强制停止应走 kill 端点，实际请求: %s", f.calls[len(f.calls)-1])
	}
}

func TestLogsTailAndTruncate(t *testing.T) {
	f := &fakeEngine{t: t, kind: "docker", logs: "l1\nl2\nl3\n\n"}
	srv, rt := f.server()
	defer srv.Close()

	res, err := rt.Logs(context.Background(), "web", LogOptions{TailLines: 2, Stdout: true, Stderr: true})
	if err != nil {
		t.Fatalf("Logs 失败: %v", err)
	}
	if len(res.Lines) != 2 || res.Lines[1] != "l3" {
		t.Fatalf("尾部截取错误: %+v", res.Lines)
	}
	if !res.Truncated {
		t.Fatal("截断后应标记 Truncated")
	}
	req := f.calls[len(f.calls)-1]
	if !strings.Contains(req, "tail=2") || !strings.Contains(req, "follow=false") {
		t.Fatalf("日志请求参数错误: %s", req)
	}
}

func TestValidateRefRejectsEmpty(t *testing.T) {
	f := &fakeEngine{t: t, kind: "docker"}
	srv, rt := f.server()
	defer srv.Close()

	if err := rt.Start(context.Background(), "  "); err == nil {
		t.Fatal("空容器引用应返回错误")
	}
	if err := rt.Start(context.Background(), "a/b"); err == nil {
		t.Fatal("含路径分隔符的引用应被拒绝")
	}
}

func TestNoneRuntime(t *testing.T) {
	rt := NewNone("")
	if !IsNone(rt) {
		t.Fatal("NewNone 应被 IsNone 识别")
	}
	info, err := rt.Info(context.Background())
	if err != nil {
		t.Fatalf("Info 不应返回错误: %v", err)
	}
	if info.Kind != KindNone {
		t.Fatalf("Kind = %q，期望 none", info.Kind)
	}
	// 空实现必须返回 ErrNone 而不是 nil/panic，上层据此回 unsupported。
	if err := rt.Start(context.Background(), "x"); !errors.Is(err, ErrNone) {
		t.Fatalf("Start 应返回 ErrNone，实际: %v", err)
	}
	if err := rt.Stop(context.Background(), "x", true); !errors.Is(err, ErrNone) {
		t.Fatalf("Stop 应返回 ErrNone，实际: %v", err)
	}
	if err := rt.Remove(context.Background(), "x", false); !errors.Is(err, ErrNone) {
		t.Fatalf("Remove 应返回 ErrNone，实际: %v", err)
	}
	if _, err := rt.Logs(context.Background(), "x", LogOptions{}); !errors.Is(err, ErrNone) {
		t.Fatalf("Logs 应返回 ErrNone，实际: %v", err)
	}
	// 无运行时下列表是空而不是错误。
	list, err := rt.List(context.Background(), true)
	if err != nil || list != nil {
		t.Fatalf("List 应返回空列表且无错误，实际: %v %v", list, err)
	}
}

func TestNewFailsOnExplicitBadSocket(t *testing.T) {
	// 显式配置了端点却连不上时必须报错，不能静默降级成"没装引擎"。
	rt, err := New(Config{Socket: "unix:///nonexistent/arkade-test.sock"})
	if err == nil {
		t.Fatalf("期望报错，实际返回运行时: %v", rt)
	}
	if !IsNone(rt) {
		t.Fatal("失败时应返回 RuntimeNone")
	}
}

func TestNewAutoDiscoverNoRuntime(t *testing.T) {
	// 自动发现时一个端点都连不上属于环境事实：返回 RuntimeNone 且无错误。
	t.Setenv("DOCKER_HOST", "")
	rt, err := New(Config{})
	if err != nil {
		t.Fatalf("自动发现失败不应返回错误: %v", err)
	}
	if rt == nil {
		t.Fatal("不应返回 nil 运行时")
	}
}

func TestNormalizeAndDedupeEndpoints(t *testing.T) {
	cases := map[string]string{
		"/var/run/docker.sock":           "unix:///var/run/docker.sock",
		"unix:///run/podman/podman.sock": "unix:///run/podman/podman.sock",
		"127.0.0.1:2375":                 "tcp://127.0.0.1:2375",
		"tcp://10.0.0.1:2376":            "tcp://10.0.0.1:2376",
	}
	for in, want := range cases {
		if got := normalizeSocket(in); got != want {
			t.Fatalf("normalizeSocket(%q) = %q，期望 %q", in, got, want)
		}
	}
	got := dedupe([]string{"a", "b", "a", ""})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("去重结果错误: %v", got)
	}
}

func TestSplitLogLines(t *testing.T) {
	got := splitLogLines("a\r\nb\n\n")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("拆行错误: %#v", got)
	}
	if splitLogLines("") != nil {
		t.Fatal("空日志应返回 nil")
	}
}
