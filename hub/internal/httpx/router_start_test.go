package httpx

// router_start_test.go 提供测试专用的启动 helper。
//
// 为什么测试也必须真实启动：ghttp 的服务端生命周期（session 管理、
// handleResponse 收尾、重复 WriteHeader 检测）只有真实 http.Server
// 之后才完整。httptest.ResponseRecorder 会绕过这些，让问题漏到生产。
//
// 没有生产侧的对应封装：runtime.go 直接持有 *ghttp.Server 并调用
// SetAddr/Start，测试也直接用同样三行，不引入只为一处调用的抽象层。

import (
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"
)

// StartForTest 启动一个 ephemeral 端口的服务，返回访问基址。
//
// 端口用 127.0.0.1:0 让内核分配，避免测试间冲突；
// Shutdown 注册在 t.Cleanup 里，保证每个测试自己收拾干净。
func StartForTest(t interface {
	Helper()
	Fatalf(string, ...any)
	Cleanup(func())
}, h http.Handler) string {
	t.Helper()

	s, ok := h.(*ghttp.Server)
	if !ok {
		t.Fatalf("NewRouter 应返回 *ghttp.Server，实际 %T", h)
	}

	s.SetAddr("127.0.0.1:0")
	if err := s.Start(); err != nil {
		t.Fatalf("启动 GoFrame 服务器失败: %v", err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })

	return "http://" + s.GetListenedAddress()
}

// startRouterForTest 是 newTestEnv 用的同义入口，读起来更贴合调用处语义。
func startRouterForTest(t interface {
	Helper()
	Fatalf(string, ...any)
	Cleanup(func())
}, h http.Handler) string {
	return StartForTest(t, h)
}
