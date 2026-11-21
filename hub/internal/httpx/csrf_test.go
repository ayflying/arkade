package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBrowserMutationIsProxyConcern 记录一个已停用的设计。
//
// 历史上 Hub 自己拦跨站写请求，但它在反向代理后面看不到真实的协议：
// 浏览器 https、代理转发明文 http，于是同源判断永远失败，
// 连登录都被 403 挡死（真实环境复现过）。前端要靠代理那边做 CSRF 防护，
// 所以这里断言"服务端不再拦"，避免有人把这段校验悄悄加回来再踩一次。
func TestBrowserMutationIsProxyConcern(t *testing.T) {
	samples := []struct {
		name, method, path, origin, fetch string
	}{
		{"登录", http.MethodPost, "/api/auth/login", "https://arkade.example", "same-origin"},
		{"代理后面协议不一致", http.MethodPost, "/api/auth/login", "https://arkade.example", "cross-site"},
		{"外部来源", http.MethodPost, "/api/account/identities/oidc/bind", "https://evil.example", ""},
		{"null origin", http.MethodPost, "/api/auth/register", "null", ""},
	}
	for _, tc := range samples {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://arkade.example"+tc.path, strings.NewReader("{}"))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.fetch)
			if !browserMutationAllowed(r) {
				t.Fatal("服务端不应再拦截写请求：Hub 在反向代理后面无法可靠判断来源")
			}
		})
	}
}

// TestSecurityHeadersKeepHardening 确认去掉跨站拦截后，其余安全响应头仍在。
func TestSecurityHeadersKeepHardening(t *testing.T) {
	h := securityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "http://arkade.example/api/auth/login", strings.NewReader("{}"))
	r.Header.Set("Origin", "https://evil.example")
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("请求被拦截：%d %s", rec.Code, rec.Body.String())
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "same-origin",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Fatalf("%s=%q want %q", header, got, want)
		}
	}
}

func TestPasswordChangeGateAllowlist(t *testing.T) {
	for _, path := range []string{"/api/auth/me", "/api/auth/logout", "/api/account", "/api/account/password"} {
		if !passwordChangeAllowed(path) {
			t.Fatalf("blocked recovery: %s", path)
		}
	}
	for _, path := range []string{"/api/settings", "/api/agents", "/api/account/identities", "/api/account/password/../identities", "/api/account/password/"} {
		if passwordChangeAllowed(path) {
			t.Fatalf("allowed resource before password change: %s", path)
		}
	}
}
