package runtime

// unix_socket_test.go 用**真实 unix 域套接字**验证客户端能通过套接字路径拨号。
//
// 为什么必须单独有这个测试：
// 其余测试都用 httptest（TCP）构造假引擎，它们能验证协议编解码，却完全绕过了
// 「怎么连上套接字」这一步。真实环境清一色走 unix socket，于是曾经出现过一个
// 致命但测试全绿的缺陷——DialContext 直接复用 net.Dialer，而 http.Transport
// 传进来的 addr 是 "docker:80"，被当作 TCP 地址解析，连接永远失败：
// 现象是容器里明明挂载了 /var/run/docker.sock，Agent 却始终报"未检测到运行时"。
//
// 这个测试用 net.Listen("unix", ...) 起一个真实套接字服务，让拨号路径无法被绕过。
// 注意：Windows 上 AF_UNIX 支持不完整，因此该测试仅在非 Windows 平台运行。

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestUnixSocketDial 验证能通过真实 unix 套接字访问 Docker 兼容 API。
func TestUnixSocketDial(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 的 AF_UNIX 支持不完整，跳过真实套接字测试")
	}

	// unix 套接字路径有长度上限（Linux 约 108 字节），临时目录可能很长，
	// 因此用 /tmp 下的短路径，测试结束自行清理。
	dir, err := os.MkdirTemp("", "ark")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	sockPath := filepath.Join(dir, "d.sock")

	mux := http.NewServeMux()
	mux.HandleFunc(pathVersion, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Version":    "29.9.2",
			"ApiVersion": "1.56",
			"Components": []map[string]any{{"Name": "Engine", "Version": "29.9.2"}},
		})
	})
	mux.HandleFunc("/containers/json", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"Id":"abc123def456","Names":["/demo"],"Image":"nginx:1.27","State":"running"}]`))
	})

	srv := &http.Server{Handler: mux}
	l, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("监听 unix 套接字失败: %v", err)
	}
	go func() { _ = srv.Serve(l) }()
	defer func() { _ = srv.Close() }()

	// 1. 端到端：probe 应能识别出 Docker 并拿到版本号。
	ctx := context.Background()
	rt, err := probe(ctx, "unix://"+sockPath, false)
	if err != nil {
		t.Fatalf("通过 unix 套接字探测失败（这正是回归点）: %v", err)
	}
	info, err := rt.Info(ctx)
	if err != nil {
		t.Fatalf("获取运行时信息失败: %v", err)
	}
	if info.Kind != KindDocker {
		t.Errorf("应识别为 Docker，实际 %q", info.Kind)
	}
	if info.Version != "29.9.2" {
		t.Errorf("版本应为 29.9.2，实际 %q", info.Version)
	}

	// 2. 业务端点也要能用同一条拨号路径。
	containers, err := rt.List(ctx, true)
	if err != nil {
		t.Fatalf("通过 unix 套接字列出容器失败: %v", err)
	}
	if len(containers) != 1 || containers[0].Name != "demo" {
		t.Fatalf("容器列表不符合预期: %+v", containers)
	}
}

// TestUnixSocketMissing 验证套接字不存在时报错信息可读，不会 panic。
func TestUnixSocketMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("仅在类 Unix 平台验证")
	}

	_, err := probe(context.Background(), "unix:///nonexistent/definitely-not-here.sock", false)
	if err == nil {
		t.Fatal("套接字不存在时必须返回错误")
	}
}
