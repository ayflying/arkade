package httpx

// identity_oidc_login_test.go 覆盖登录闭环与跨站请求防护。
//
// 这些用例的共同点：验证"一次完整登录能不能安全地做成"。
// 环境装配与请求工具在 identity_oidc_e2e_test.go，
// 假提供方在 identity_oidc_provider_test.go。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"arkade/hub/internal/user"
)

// oidcLoginResult 是一次完整 OIDC 登录的结果。
type oidcLoginResult struct {
	session  *http.Cookie // 会话 Cookie
	location string       // callback 的 302 目标
}

// runOIDCLogin 走完 start → 提供方授权 → callback，返回会话 Cookie。
func (e *oidcTestEnv) runOIDCLogin(t *testing.T, providerID string) oidcLoginResult {
	t.Helper()

	location, browser := e.beginOIDC(t, providerID, nil)
	callbackURL := e.authorizeAtProvider(t, location)

	// 回调必须带 start 下发的绑定 Cookie，否则会被当成"不是同一个浏览器"。
	var cookies []*http.Cookie
	if browser != nil {
		cookies = append(cookies, browser)
	}
	resp := e.get(t, pathAndQuery(t, callbackURL), cookies)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback 期望 302，实际 %d：%s", resp.StatusCode, e.body(resp))
	}
	target := resp.Header.Get("Location")
	session := findCookie(resp, "arkade_session")
	_ = e.body(resp)
	return oidcLoginResult{session: session, location: target}
}

// userIDOf 用会话 Cookie 查当前用户 ID。
func (e *oidcTestEnv) userIDOf(t *testing.T, session *http.Cookie) string {
	t.Helper()
	resp := e.get(t, "/api/auth/me", []*http.Cookie{session})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("me 期望 200，实际 %d：%s", resp.StatusCode, e.body(resp))
	}
	me := e.jsonBody(t, resp)
	view, _ := me["user"].(map[string]any)
	uid, _ := view["id"].(string)
	if uid == "" {
		t.Fatalf("me 响应缺 user.id: %v", me)
	}
	return uid
}

// TestOIDCLoginCreatesSessionAndNormalUser 验证完整登录闭环。
//
// 一次登录同时证明：会话下发、me 是普通用户、用户名由本地生成、
// 不泄漏密码哈希、新用户看不到任何服务器。
func TestOIDCLoginCreatesSessionAndNormalUser(t *testing.T) {
	e := newOIDCTestEnv(t)
	id := e.createProvider(t, true)

	out := e.runOIDCLogin(t, id)
	if out.session == nil {
		t.Fatalf("callback 没有下发会话 Cookie，跳转到 %q", out.location)
	}
	if strings.Contains(out.location, "oidc_error") {
		t.Fatalf("callback 跳到了失败页: %s", out.location)
	}

	meResp := e.get(t, "/api/auth/me", []*http.Cookie{out.session})
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("me 期望 200，实际 %d：%s", meResp.StatusCode, e.body(meResp))
	}
	rawMe := e.body(meResp)
	me := map[string]any{}
	if err := json.Unmarshal([]byte(rawMe), &me); err != nil {
		t.Fatalf("me 不是 JSON: %s", rawMe)
	}
	view, _ := me["user"].(map[string]any)
	if view == nil {
		t.Fatalf("me 响应缺 user 字段: %v", me)
	}
	if role, _ := view["role"].(string); role != string(user.RoleUser) {
		t.Fatalf("OIDC 新用户角色应为普通用户，实际 %q", role)
	}
	// 用户名由本地生成，不来自提供方声明。
	if name, _ := view["username"].(string); !strings.HasPrefix(name, "oidc_") {
		t.Fatalf("OIDC 用户名应由本地生成（oidc_ 前缀），实际 %q", name)
	}
	// 只允许 local_password（"是否设了本地密码"的布尔摘要），不允许哈希。
	lower := strings.ToLower(rawMe)
	if strings.Contains(lower, "password_hash") || strings.Contains(lower, "passwordhash") {
		t.Fatalf("me 响应泄漏密码哈希: %s", rawMe)
	}
	if lp, ok := view["local_password"].(bool); ok && lp {
		t.Fatalf("OIDC 新建账号不应有本地密码: %s", rawMe)
	}

	// 新用户没有服务器授权，agents 必须是空数组（不是 null、不是 403）。
	agResp := e.get(t, "/api/agents", []*http.Cookie{out.session})
	if agResp.StatusCode != http.StatusOK {
		t.Fatalf("agents 期望 200，实际 %d：%s", agResp.StatusCode, e.body(agResp))
	}
	agBody := e.body(agResp)
	if !strings.Contains(agBody, "\"agents\"") {
		t.Fatalf("agents 响应结构异常: %s", agBody)
	}
	if strings.Contains(agBody, "\"id\"") {
		t.Fatalf("新用户不应看到任何服务器: %s", agBody)
	}
}

// TestOIDCRejectsStateReplay 同一 state 不能完成两次登录。
func TestOIDCRejectsStateReplay(t *testing.T) {
	e := newOIDCTestEnv(t)
	id := e.createProvider(t, true)

	location, browser := e.beginOIDC(t, id, nil)
	callPath := pathAndQuery(t, e.authorizeAtProvider(t, location))
	cookies := []*http.Cookie{browser}

	first := e.get(t, callPath, cookies)
	firstTarget := first.Header.Get("Location")
	firstSession := findCookie(first, "arkade_session")
	_ = e.body(first)
	if firstSession == nil || strings.Contains(firstTarget, "oidc_error") {
		t.Fatalf("首次 callback 本应成功，实际 %s", firstTarget)
	}

	// 重放同一个 state+code：必须被拒，且不下发会话。
	second := e.get(t, callPath, cookies)
	secondBody := e.body(second)
	if second.StatusCode != http.StatusFound {
		t.Fatalf("重放期望 302 跳失败页，实际 %d：%s", second.StatusCode, secondBody)
	}
	if !strings.Contains(second.Header.Get("Location"), "oidc_error") {
		t.Fatalf("重放应跳到失败页，实际 %s", second.Header.Get("Location"))
	}
	if findCookie(second, "arkade_session") != nil {
		t.Fatal("重放不应下发新的会话 Cookie")
	}
}

// TestOIDCRejectsMissingBrowserCookie 缺浏览器绑定 Cookie 时必须拒绝。
//
// 这道防线防的是登录 CSRF：没有 start 下发的 Cookie
// 就无法证明回调是同一浏览器发起的。
func TestOIDCRejectsMissingBrowserCookie(t *testing.T) {
	e := newOIDCTestEnv(t)
	id := e.createProvider(t, true)

	location, browser := e.beginOIDC(t, id, nil)
	if browser == nil {
		t.Fatal("start 未下发绑定 Cookie，无法验证缺失场景")
	}
	callbackURL := e.authorizeAtProvider(t, location)

	resp := e.get(t, pathAndQuery(t, callbackURL), nil)
	body := e.body(resp)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("缺 Cookie 期望 302 跳失败页，实际 %d：%s", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Location"), "oidc_error") {
		t.Fatalf("缺 Cookie 应跳失败页，实际 %s", resp.Header.Get("Location"))
	}
	if findCookie(resp, "arkade_session") != nil {
		t.Fatal("缺绑定 Cookie 却下发了会话，登录 CSRF 防线失效")
	}
}

// TestOIDCRejectsWrongBrowserCookie 换另一个 state 的绑定 Cookie 也必须拒绝。
//
// 比"完全缺失"更严格：证明校验的是这个 state 对应的值，
// 而不是"只要带了 Cookie 就放行"。
//
// 注意：这里**不**用正确的 Cookie 重试同一条 flow。service 的语义是
// "先消费 state 再校验"（flow.go 注释 "Consume before network operations"），
// 任何一次回调——无论成败——都会作废该 state。这是既定的重放防护，
// 所以改用一条全新流程来确认正确 Cookie 依然可用。
func TestOIDCRejectsWrongBrowserCookie(t *testing.T) {
	e := newOIDCTestEnv(t)
	id := e.createProvider(t, true)

	loc1, _ := e.beginOIDC(t, id, nil)
	cb1 := pathAndQuery(t, e.authorizeAtProvider(t, loc1))

	_, browser2 := e.beginOIDC(t, id, nil)
	if browser2 == nil {
		t.Fatal("第二次 start 未下发绑定 Cookie")
	}

	// 用 state2 的 Cookie 去回调 state1。
	resp := e.get(t, cb1, []*http.Cookie{browser2})
	body := e.body(resp)
	if resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), "oidc_error") {
		t.Fatalf("跨 state 的 Cookie 应被拒，实际 %d -> %s：%s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	if findCookie(resp, "arkade_session") != nil {
		t.Fatal("跨 state Cookie 却下发了会话")
	}

	// 用全新流程确认正确 Cookie 仍可用，排除"全都拒"的假阳性。
	loc3, browser3 := e.beginOIDC(t, id, nil)
	ok := e.get(t, pathAndQuery(t, e.authorizeAtProvider(t, loc3)), []*http.Cookie{browser3})
	okTarget := ok.Header.Get("Location")
	okSession := findCookie(ok, "arkade_session")
	_ = e.body(ok)
	if okSession == nil || strings.Contains(okTarget, "oidc_error") {
		t.Fatalf("正确的绑定 Cookie 本应成功，实际 %s", okTarget)
	}
}
