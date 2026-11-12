package httpx

// identity_oidc_settings_test.go 覆盖设置侧的安全边界。
//
// 注册开关的独立性、provider 的权限与密钥边界。
// 环境装配与请求工具在 identity_oidc_e2e_test.go。

import (
	"net/http"
	"strings"
	"testing"
)

// TestOIDCRegistrationToggleIsIndependent 注册开关不影响 OIDC 登录。
//
// 两道门独立：registration_enabled 管本地自助注册，
// provider 的 auto_register 管外部身份首次登录能否建号。
func TestOIDCRegistrationToggleIsIndependent(t *testing.T) {
	e := newOIDCTestEnv(t)

	// GET 返回 {settings:{...}}，PUT 接受平铺的 SettingsView + version，
	// 且必须整体提交：只传一个字段会被判成"要关掉本地密码登录"而拒绝。
	getBody := e.jsonBody(t, e.get(t, "/api/settings", []*http.Cookie{e.adminCookie}))
	settings, _ := getBody["settings"].(map[string]any)
	if settings == nil {
		t.Fatalf("settings 响应结构异常: %v", getBody)
	}
	settings["registration_enabled"] = false
	settings["local_login_enabled"] = true
	putResp := e.do(t, http.MethodPut, "/api/settings", mustJSON(t, settings),
		[]*http.Cookie{e.adminCookie}, nil)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("关闭本地注册失败: %d %s", putResp.StatusCode, e.body(putResp))
	}
	_ = e.body(putResp)

	// 第一道门：本地注册确实被关。
	regResp := e.post(t, "/api/auth/register", map[string]any{
		"username": "local-new", "password": "TestPass123!", "displayName": "本地新用户",
	}, nil)
	if regResp.StatusCode == http.StatusOK {
		t.Fatalf("关闭注册后本地注册仍成功: %s", e.body(regResp))
	}
	_ = e.body(regResp)

	// 第二道门独立：provider 的 auto_register 仍决定外部首次登录能否建号。
	// 关：首次登录必须被拒。
	strict := e.createProvider(t, false)
	denied := e.runOIDCLogin(t, strict)
	if !strings.Contains(denied.location, "oidc_error") {
		t.Fatalf("auto_register=false 时首次 OIDC 登录本应失败，实际跳转 %s", denied.location)
	}
	if denied.session != nil {
		t.Fatal("auto_register=false 却下发了会话")
	}

	// 开：外部首次登录成功，不受本地注册开关影响。
	open := e.createProvider(t, true)
	out := e.runOIDCLogin(t, open)
	if out.session == nil || strings.Contains(out.location, "oidc_error") {
		t.Fatalf("auto_register=true 时外部首次登录应成功，实际跳转 %q", out.location)
	}

	// options 报告的开关状态必须与实际一致。
	opt := e.jsonBody(t, e.get(t, "/api/auth/options", nil))
	if enabled, ok := opt["registration_enabled"].(bool); !ok || enabled {
		t.Fatalf("options 应报告 registration_enabled=false，实际 %v", opt["registration_enabled"])
	}
}

// TestOIDCProviderRequiresAdmin 非管理员不能改 provider 配置。
func TestOIDCProviderRequiresAdmin(t *testing.T) {
	e := newOIDCTestEnv(t)
	firstID := e.createProvider(t, true)

	login := e.runOIDCLogin(t, firstID)
	if login.session == nil {
		t.Fatal("普通用户登录失败")
	}

	resp := e.post(t, "/api/settings/oidc-providers", map[string]any{
		"display_name": "越权", "issuer": e.provider.URL(),
		"client_id": "x", "client_secret": "y", "enabled": true,
	}, []*http.Cookie{login.session})
	body := e.body(resp)
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("普通用户创建 provider 应被拒，实际 %d：%s", resp.StatusCode, body)
	}
}

// TestOIDCProviderSecretNeverReturned 读 provider 时不返回明文 secret。
func TestOIDCProviderSecretNeverReturned(t *testing.T) {
	e := newOIDCTestEnv(t)
	e.createProvider(t, false)

	resp := e.get(t, "/api/settings/oidc-providers", []*http.Cookie{e.adminCookie})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("列表期望 200，实际 %d：%s", resp.StatusCode, e.body(resp))
	}
	body := e.body(resp)
	if strings.Contains(body, "test-secret") {
		t.Fatalf("provider 列表泄漏明文 secret: %s", body)
	}
	if !strings.Contains(body, "has_client_secret") {
		t.Fatalf("provider 列表应说明是否有 secret: %s", body)
	}
}

// TestOIDCProviderRejectsInsecureIssuer 非 loopback 的 http issuer 必须被拒。
func TestOIDCProviderRejectsInsecureIssuer(t *testing.T) {
	e := newOIDCTestEnv(t)

	resp := e.post(t, "/api/settings/oidc-providers", map[string]any{
		"display_name": "明文端点", "issuer": "http://example.com",
		"client_id": "x", "client_secret": "y", "enabled": true,
	}, []*http.Cookie{e.adminCookie})
	body := e.body(resp)
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		t.Fatalf("http 非 loopback issuer 不应被接受: %s", body)
	}
}
