package httpx

// enroll_test.go 覆盖「预创建服务器 + 专属接入密钥」的正向路径与生命周期。
//
// 拒绝矩阵见 enroll_deny_test.go，撤销竞态见 enroll_race_test.go。
// 这里只放"应该成功"和"状态/凭据随生命周期变化"的用例。
//
// 全部走**真实 WebSocket + 真实 PostgreSQL**，并与 API 共用同一个服务与同一份
// registry/guard（与生产装配一致）。若是各建一套，测得再绿也不代表真实行为。

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/store"
)

// enrollCreateResp 是新建/重置服务器的响应。
type enrollCreateResp struct {
	Agent struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		DisplayName string `json:"display_name"`
		Note        string `json:"note"`
		Status      string `json:"status"`
		Disabled    bool   `json:"disabled"`
	} `json:"agent"`
	Enrollment struct {
		Token   string `json:"token"`
		AgentID string `json:"agent_id"`
	} `json:"enrollment"`
}

// createServer 调用新建接口并返回响应。
func (e *testEnv) createServer(t *testing.T, cookies []*http.Cookie, body string) (int, enrollCreateResp, string) {
	t.Helper()
	rec, _ := e.do(t, http.MethodPost, "/api/agents", body, cookies)
	var out enrollCreateResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Body.String()
}

// TestCreateAgentReturnsEnrollment 验证新建返回 201 与一次性密钥。
func TestCreateAgentReturnsEnrollment(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	code, resp, raw := e.createServer(t, cookies, `{"name":"生产-东京","note":"首批","labels":{"env":"prod"}}`)
	if code != http.StatusCreated {
		t.Fatalf("应返回 201，实际 %d，响应 %s", code, raw)
	}
	if resp.Agent.ID == "" {
		t.Fatal("必须返回服务器 id")
	}
	if resp.Agent.Status != store.StatusPending {
		t.Errorf("预建记录状态应为 pending，实际 %q", resp.Agent.Status)
	}
	// display_name 沿用现有约定：未单独设置时回落为 name。
	if resp.Agent.DisplayName != "生产-东京" {
		t.Errorf("display_name 应回落为名称，实际 %q", resp.Agent.DisplayName)
	}
	if resp.Agent.Note != "首批" {
		t.Errorf("备注未保存，实际 %q", resp.Agent.Note)
	}
	if resp.Enrollment.AgentID != resp.Agent.ID {
		t.Errorf("enrollment.agent_id 应与 agent.id 一致：%q vs %q",
			resp.Enrollment.AgentID, resp.Agent.ID)
	}
	// 密钥长度：32 字节 base64url 无填充 = 43 字符。
	if len(resp.Enrollment.Token) < 43 {
		t.Errorf("密钥过短（应至少 32 随机字节），实际长度 %d", len(resp.Enrollment.Token))
	}
}

// TestCreateAgentLabelsPreserved 验证新建时填的标签被保存。
func TestCreateAgentLabelsPreserved(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	_, resp, raw := e.createServer(t, cookies, `{"name":"标签机","labels":{"env":"prod","rack":"b3"}}`)
	if resp.Agent.ID == "" {
		t.Fatalf("创建失败: %s", raw)
	}
	rec, err := e.db.GetAgent(t.Context(), resp.Agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Labels["env"] != "prod" || rec.Labels["rack"] != "b3" {
		t.Errorf("标签未保存: %+v", rec.Labels)
	}
}

// TestEnrollmentResponseNoStore 验证含密钥的响应禁止缓存。
//
// 密钥一旦被浏览器缓存或被中间代理留存，就等价于把凭据写进了
// 一个用户清不掉、我们也看不到的地方。
func TestEnrollmentResponseNoStore(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	rec, _ := e.do(t, http.MethodPost, "/api/agents", `{"name":"cache-test"}`, cookies)
	if rec.Code != http.StatusCreated {
		t.Fatalf("实际 %d: %s", rec.Code, rec.Body.String())
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") {
		t.Errorf("创建响应必须 Cache-Control: no-store，实际 %q", cc)
	}
	if pragma := rec.Header().Get("Pragma"); pragma != "no-cache" {
		t.Errorf("创建响应应带 Pragma: no-cache，实际 %q", pragma)
	}

	// 重置响应同样含密钥，同样不能缓存。
	var created enrollCreateResp
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	rec2, _ := e.do(t, http.MethodPost,
		"/api/agents/"+created.Agent.ID+"/token/reset", "", cookies)
	if rec2.Code != http.StatusOK {
		t.Fatalf("重置失败 %d", rec2.Code)
	}
	if cc := rec2.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("重置响应必须 no-store，实际 %q", cc)
	}
}

// TestEnrollmentTokenNotRetrievable 验证密钥无法再次取回。
//
// 只在创建响应里出现一次。列表、详情都不能带——
// 那等于把凭据存进了一个会被日志和备份带走的地方。
func TestEnrollmentTokenNotRetrievable(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	code, resp, raw := e.createServer(t, cookies, `{"name":"tok-test"}`)
	if code != http.StatusCreated {
		t.Fatalf("实际 %d：%s", code, raw)
	}
	token := resp.Enrollment.Token
	hash := hashOf(token)

	// 列表里既不能有明文，也不能有摘要。
	listRec, _ := e.do(t, http.MethodGet, "/api/agents", "", cookies)
	listBody := listRec.Body.String()
	if strings.Contains(listBody, token) {
		t.Error("列表响应里出现了明文密钥")
	}
	if strings.Contains(listBody, hash) {
		t.Error("列表响应里出现了密钥摘要")
	}
	if strings.Contains(raw, hash) {
		t.Error("创建响应里除了明文不该再带摘要")
	}
}

// TestEnrollmentTokenStoredAsHashOnly 验证库里只有摘要，没有明文。
func TestEnrollmentTokenStoredAsHashOnly(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	_, resp, _ := e.createServer(t, cookies, `{"name":"hash-test"}`)
	token := resp.Enrollment.Token

	// 按摘要能查到。
	id, err := e.db.LookupAgentByTokenHash(t.Context(), hashOf(token))
	if err != nil {
		t.Fatalf("按摘要应能查到绑定身份: %v", err)
	}
	if id != resp.Agent.ID {
		t.Errorf("绑定身份错误：%q != %q", id, resp.Agent.ID)
	}
	// 拿明文当摘要查必然失败，反证库里存的确实是哈希后的值。
	if _, err := e.db.LookupAgentByTokenHash(t.Context(), token); err == nil {
		t.Error("明文本身不应能查到记录（说明没有哈希）")
	}
}

// TestCreateAgentRequiresAdmin 验证权限：401 / 403。
func TestCreateAgentRequiresAdmin(t *testing.T) {
	e := newTestEnv(t)

	// 未登录 → 401。
	rec, _ := e.do(t, http.MethodPost, "/api/agents", `{"name":"x"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("未登录应 401，实际 %d", rec.Code)
	}

	// 普通用户 → 403。
	adminCookies := e.loginAs(t, "admin", testPassword)
	e.createUser(t, adminCookies, "normal", "NormalPass123!", "user")
	cookies := e.loginAs(t, "normal", "NormalPass123!")
	rec, _ = e.do(t, http.MethodPost, "/api/agents", `{"name":"x"}`, cookies)
	if rec.Code != http.StatusForbidden {
		t.Errorf("普通用户应 403，实际 %d", rec.Code)
	}
}

// TestResetTokenRequiresAdmin 验证重置也只允许 admin。
func TestResetTokenRequiresAdmin(t *testing.T) {
	e := newTestEnv(t)
	adminCookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, adminCookies, `{"name":"reset-perm"}`)

	e.createUser(t, adminCookies, "normal2", "NormalPass123!", "user")
	cookies := e.loginAs(t, "normal2", "NormalPass123!")
	rec, _ := e.do(t, http.MethodPost,
		"/api/agents/"+created.Agent.ID+"/token/reset", "", cookies)
	if rec.Code != http.StatusForbidden {
		t.Errorf("普通用户应 403，实际 %d", rec.Code)
	}
}

// TestCreateAgentDuplicateName 验证重名 409。
func TestCreateAgentDuplicateName(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	if code, _, raw := e.createServer(t, cookies, `{"name":"重复名"}`); code != http.StatusCreated {
		t.Fatalf("首次创建应成功，实际 %d：%s", code, raw)
	}
	code, _, raw := e.createServer(t, cookies, `{"name":"重复名"}`)
	if code != http.StatusConflict {
		t.Errorf("重名应 409，实际 %d：%s", code, raw)
	}
	// 大小写与空白差异也算重名：界面上一眼看不出区别，
	// 允许它们共存会让用户无法分辨哪条是哪台机器。
	code, _, _ = e.createServer(t, cookies, `{"name":"  重复名  "}`)
	if code != http.StatusConflict {
		t.Errorf("规范化后重名应 409，实际 %d", code)
	}
}

// TestCreateAgentRejectsBlankName 验证空名被拒。
func TestCreateAgentRejectsBlankName(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	for _, body := range []string{`{"name":""}`, `{"name":"   "}`, `{}`} {
		code, _, _ := e.createServer(t, cookies, body)
		if code != http.StatusBadRequest {
			t.Errorf("请求体 %s 应 400，实际 %d", body, code)
		}
	}
}

// TestEnrolledTokenBindsIdentity 验证专属密钥按库中身份绑定。
//
// 核心断言：Header 里声明的 agent_id 完全不起作用。
func TestEnrolledTokenBindsIdentity(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"bind-test"}`)

	// 故意在 Header 里写一个**别的** ID，模拟"试图冒用另一条记录"。
	conn, ack := dialWith(t, e.wsURL, created.Enrollment.Token, "agent-somebody-else", "冒名主机")
	_ = conn.Close()

	if ack.AgentID != created.Agent.ID {
		t.Fatalf("身份应以数据库绑定为准 %q，实际 %q（说明 Header 里的 ID 被采纳了）",
			created.Agent.ID, ack.AgentID)
	}
	if e.registry.IsOnline("agent-somebody-else") {
		t.Error("不应出现以伪造 ID 为键的在线连接")
	}
}

// TestResetInvalidatesOldToken 验证重置后旧密钥立即失效并踢连接。
func TestResetInvalidatesOldToken(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"reset-test"}`)
	oldToken := created.Enrollment.Token

	// 先用旧密钥连上。
	conn := dialOnly(t, e.wsURL, oldToken)
	defer func() { _ = conn.Close() }()
	if resp, err := sendHello(t, conn); err != nil || resp.Error != nil {
		t.Fatalf("旧密钥应能接入: %+v %v", resp, err)
	}
	if !e.registry.IsOnline(created.Agent.ID) {
		t.Fatal("旧密钥应能上线")
	}

	// 重置。
	rec, _ := e.do(t, http.MethodPost,
		"/api/agents/"+created.Agent.ID+"/token/reset", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("重置应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}
	var resetResp enrollCreateResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resetResp); err != nil {
		t.Fatal(err)
	}
	newToken := resetResp.Enrollment.Token
	if newToken == oldToken {
		t.Fatal("重置后的密钥必须与旧的不同")
	}

	// 旧连接应被踢掉：否则"重置了密钥但旧连接还能用"，重置看起来毫无效果。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !e.registry.IsOnline(created.Agent.ID) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e.registry.IsOnline(created.Agent.ID) {
		t.Error("重置密钥后应踢掉现有连接")
	}

	// 旧密钥不能再接入。
	expectHandshakeRejected(t, e.wsURL, oldToken)

	// 新密钥可以接入并绑定同一记录。
	conn2, ack2 := dialWith(t, e.wsURL, newToken, "", "reset-host")
	defer func() { _ = conn2.Close() }()
	if ack2.AgentID != created.Agent.ID {
		t.Errorf("新密钥应绑定同一记录：%q != %q", ack2.AgentID, created.Agent.ID)
	}
}

// TestDeleteInvalidatesToken 验证删除服务器后旧密钥不能再注册。
func TestDeleteInvalidatesToken(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"delete-test"}`)
	token := created.Enrollment.Token

	rec, _ := e.do(t, http.MethodDelete, "/api/agents/"+created.Agent.ID, "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除应成功，实际 %d：%s", rec.Code, rec.Body.String())
	}

	// 旧密钥不能再把记录建回来。
	expectHandshakeRejected(t, e.wsURL, token)

	if _, err := e.db.GetAgent(t.Context(), created.Agent.ID); err == nil {
		t.Error("记录应已被删除，且不能被重连重建")
	}
}

// TestEnrolledAgentBecomesOnline 验证接入后状态从 pending 变成 online。
func TestEnrolledAgentBecomesOnline(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, created, _ := e.createServer(t, cookies, `{"name":"online-test","note":"管理员备注","labels":{"region":"home"}}`)

	// 创建后是 pending。
	rec, _ := e.do(t, http.MethodGet, "/api/agents", "", cookies)
	var list struct {
		Agents []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range list.Agents {
		if a.ID == created.Agent.ID {
			found = true
			if a.Status != store.StatusPending {
				t.Errorf("预建未接入时应为 pending，实际 %q", a.Status)
			}
		}
	}
	if !found {
		t.Fatal("新建的服务器应出现在列表里")
	}

	conn, _ := dialWith(t, e.wsURL, created.Enrollment.Token, "", "online-host")
	defer func() { _ = conn.Close() }()

	// 接入后变成 online。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ag, err := e.db.GetAgent(t.Context(), created.Agent.ID)
		if err == nil && ag.Status == "online" {
			if ag.DisplayName != "online-test" || ag.Note != "管理员备注" || ag.Labels["region"] != "home" {
				t.Fatal("首次接入覆盖了管理员预建的名称、备注或标签")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	ag, _ := e.db.GetAgent(t.Context(), created.Agent.ID)
	t.Errorf("接入后状态应为 online，实际 %q", ag.Status)
}

// ---- 辅助 ----

// sharedTestToken 是测试环境的共享 token。
const sharedTestToken = composeTestAgentToken

// hashOf 计算密钥摘要（复用生产算法，保证与 Hub 一致）。
func hashOf(token string) string {
	return hashEnrollmentTokenForTest(token)
}
