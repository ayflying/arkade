package httpx

// identity_oidc_security_test.go 覆盖身份绑定的安全边界。
//
// 三条主线索：绑定不换会话、不同 sub 不按 email 合并、审计不落凭据。
// 环境装配与请求工具在 identity_oidc_e2e_test.go。

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"arkade/hub/internal/audit"
	"arkade/hub/internal/store"
)

// TestOIDCBindKeepsExistingSession 已登录用户绑定外部身份不换会话。
//
// 绑定是给当前账号加一个登录方式，不是换个人登录；
// 若这里发了新会话，用户会被踢下线。
func TestOIDCBindKeepsExistingSession(t *testing.T) {
	e := newOIDCTestEnv(t)
	id := e.createProvider(t, true)
	before := e.adminToken

	resp := e.post(t, "/api/account/identities/"+id+"/bind", map[string]any{
		"password": "TestPass123!", "redirect": "/account",
	}, []*http.Cookie{e.adminCookie})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bind 期望 200，实际 %d：%s", resp.StatusCode, e.body(resp))
	}
	body := e.jsonBody(t, resp)
	authURL, _ := body["authorization_url"].(string)
	if authURL == "" {
		t.Fatalf("bind 未返回授权地址: %v", body)
	}
	var bindCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "arkade_oidc_") {
			bindCookie = c
			break
		}
	}
	if bindCookie == nil {
		t.Fatal("bind 未下发 state 绑定 Cookie")
	}

	cbURL := e.authorizeAtProvider(t, authURL)
	cb := e.get(t, pathAndQuery(t, cbURL), []*http.Cookie{e.adminCookie, bindCookie})
	cbTarget := cb.Header.Get("Location")
	newSession := findCookie(cb, "arkade_session")
	_ = e.body(cb)

	if strings.Contains(cbTarget, "oidc_error") {
		t.Fatalf("绑定回调失败: %s", cbTarget)
	}
	if newSession != nil && newSession.Value != before {
		t.Fatalf("绑定不应更换会话：原 %s，新 %s", before, newSession.Value)
	}

	// 原会话仍有效，身份仍是管理员（没被换成外部用户）。
	meResp := e.get(t, "/api/auth/me", []*http.Cookie{e.adminCookie})
	if meResp.StatusCode != http.StatusOK {
		t.Fatalf("绑定后原会话失效: %d", meResp.StatusCode)
	}
	me := e.jsonBody(t, meResp)
	view, _ := me["user"].(map[string]any)
	if uid, _ := view["id"].(string); uid != "u-admin" {
		t.Fatalf("绑定后身份变了：期望 u-admin，实际 %q", uid)
	}

	listResp := e.get(t, "/api/account/identities", []*http.Cookie{e.adminCookie})
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("identities 期望 200，实际 %d：%s", listResp.StatusCode, e.body(listResp))
	}
	if listBody := e.body(listResp); !strings.Contains(listBody, id) {
		t.Fatalf("绑定后身份列表里没有该 provider: %s", listBody)
	}
}

// TestOIDCExistingSubjectDoesNotMergeByEmail 同 sub 复用账号，不同 sub 不按 email 合并。
//
// 后者是安全关键：claim 里的 email 从未被信任，
// 不能凭"邮箱一样"把两个外部身份并到一个账号上。
func TestOIDCExistingSubjectDoesNotMergeByEmail(t *testing.T) {
	e := newOIDCTestEnv(t)
	id := e.createProvider(t, true)

	first := e.runOIDCLogin(t, id)
	if first.session == nil {
		t.Fatal("首次登录失败")
	}
	uid1 := e.userIDOf(t, first.session)

	second := e.runOIDCLogin(t, id)
	if second.session == nil {
		t.Fatal("同 sub 二次登录失败")
	}
	if uid2 := e.userIDOf(t, second.session); uid1 != uid2 {
		t.Fatalf("同一 sub 应复用同一账号，实际 %s vs %s", uid1, uid2)
	}

	// 换 sub：必须得到另一个账号。
	e.provider.mu.Lock()
	e.provider.subject = "another-person"
	e.provider.consumed = false
	e.provider.mu.Unlock()

	third := e.runOIDCLogin(t, id)
	if third.session == nil {
		t.Fatal("换 sub 后登录失败")
	}
	if uid3 := e.userIDOf(t, third.session); uid3 == uid1 {
		t.Fatalf("不同 sub 被合并到同一账号 %s（email 匹配不应生效）", uid1)
	}
}

// TestOIDCAuditHidesSecrets 审计记录里不能出现 code / token / secret。
//
// 审计要能复盘"谁在何时登录"，但不能落凭据：
// code 或 id_token 一旦进审计表，任何能看审计的人都能复用登录。
func TestOIDCAuditHidesSecrets(t *testing.T) {
	e := newOIDCTestEnv(t)
	id := e.createProvider(t, true)

	location, browser := e.beginOIDC(t, id, nil)
	cb := e.get(t, pathAndQuery(t, e.authorizeAtProvider(t, location)), []*http.Cookie{browser})
	session := findCookie(cb, "arkade_session")
	_ = e.body(cb)
	if cb.StatusCode != http.StatusFound || session == nil {
		t.Fatalf("登录未完成，无法验证审计：%d", cb.StatusCode)
	}

	records, _, err := e.db.ListAuditLogs(context.Background(), store.AuditFilters{Limit: 200})
	if err != nil {
		t.Fatalf("读取审计失败: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("登录没有产生任何审计记录")
	}

	// 登录审计必须存在，否则"审计不泄密"无从谈起。
	var hasLoginAudit bool
	for _, r := range records {
		if r.Action == audit.ActionUserLogin {
			hasLoginAudit = true
			break
		}
	}
	if !hasLoginAudit {
		t.Fatal("没有登录审计记录")
	}

	sensitive := []string{fakeOIDCCode, "test-secret", "fake-access-token", session.Value}
	for _, r := range records {
		joined := r.ID + r.UserID + r.Username + r.AgentID + r.AgentName +
			r.Action + r.Target + r.Detail + r.Result + r.Error
		for _, s := range sensitive {
			if s != "" && strings.Contains(joined, s) {
				t.Fatalf("审计含敏感内容 %q：action=%s target=%s detail=%s error=%s",
					s, r.Action, r.Target, r.Detail, r.Error)
			}
		}
		// eyJ 是 JWT 标准开头，审计里不该出现 id_token。
		if strings.Contains(joined, "eyJ") {
			t.Fatalf("审计含 JWT：action=%s detail=%s", r.Action, r.Detail)
		}
	}
}

// TestOIDCBindRequiresSession 绑定入口必须要求已登录（401，不是 400/500）。
func TestOIDCBindRequiresSession(t *testing.T) {
	e := newOIDCTestEnv(t)
	id := e.createProvider(t, true)

	resp := e.post(t, "/api/account/identities/"+id+"/bind", map[string]any{
		"password": "TestPass123!", "redirect": "/account",
	}, nil)
	if body := e.body(resp); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录调 bind 期望 401，实际 %d：%s", resp.StatusCode, body)
	}
}

// TestOIDCIdentityCountStaysOne 重复登录不重复写身份绑定。
func TestOIDCIdentityCountStaysOne(t *testing.T) {
	e := newOIDCTestEnv(t)
	id := e.createProvider(t, true)

	first := e.runOIDCLogin(t, id)
	if first.session == nil {
		t.Fatal("登录失败")
	}
	uid := e.userIDOf(t, first.session)

	count := func() int {
		list, err := e.db.ListUserIdentities(context.Background(), uid)
		if err != nil {
			t.Fatalf("读身份失败: %v", err)
		}
		return len(list)
	}
	if n := count(); n != 1 {
		t.Fatalf("首次登录后身份数应为 1，实际 %d", n)
	}
	for i := 0; i < 2; i++ {
		if out := e.runOIDCLogin(t, id); out.session == nil {
			t.Fatal("二次登录失败")
		}
	}
	if n := count(); n != 1 {
		t.Fatalf("重复登录后身份数应仍为 1，实际 %d（重复写入）", n)
	}
}
