package httpx

// handlers_audit_test.go 覆盖审计查询接口。
//
// 覆盖策略：
//   - 任务验收标准逐条对应：非 admin 只能看自己的、筛选生效、分页正确；
//   - 401 / 400 参数非法的边界；
//   - 端到端：真实操作产生真实审计，再从接口读出来（而不是直接塞库伪造）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// auditListResp 是列表响应结构。
type auditListResp struct {
	Total int `json:"total"`
	Items []struct {
		ID          string `json:"id"`
		UserID      string `json:"user_id"`
		Username    string `json:"username"`
		AgentID     string `json:"agent_id"`
		Action      string `json:"action"`
		ActionLabel string `json:"action_label"`
		Target      string `json:"target"`
		Result      string `json:"result"`
		Error       string `json:"error"`
		DurationMs  int64  `json:"duration_ms"`
		CreatedAt   int64  `json:"created_at"`
	} `json:"items"`
}

// getAudit 请求审计列表并解析。
func (e *testEnv) getAudit(t *testing.T, query string, cookies []*http.Cookie) (*httptest.ResponseRecorder, auditListResp) {
	t.Helper()
	rec, _ := e.do(t, http.MethodGet, "/api/audit"+query, "", cookies)
	var out auditListResp
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("解析审计响应失败: %v，响应 %s", err, rec.Body.String())
		}
	}
	return rec, out
}

// TestAuditEndToEnd 验证真实操作会被记录并能从接口读出来。
//
// 这条测试串起了中间件与查询接口：如果中间件没挂上、
// 或者列表查询的字段映射错位，它都会失败。
func TestAuditEndToEnd(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	// 产生一个真实的操作。
	if rec, _ := e.do(t, http.MethodPost, "/api/users",
		`{"username":"audited","display_name":"x","password":"Password123!","role":"user"}`, cookies); rec.Code != http.StatusOK {
		t.Fatalf("创建用户应成功，实际 %d", rec.Code)
	}

	rec, list := e.getAudit(t, "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("审计列表应可访问，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if list.Total < 1 {
		t.Fatalf("应至少记录 1 条，实际 %d", list.Total)
	}

	// 找到创建用户那条。
	found := false
	for _, item := range list.Items {
		if item.Action != "user.create" {
			continue
		}
		found = true
		if item.Username != "admin" {
			t.Errorf("操作人应为 admin，实际 %q", item.Username)
		}
		if item.Result != "success" {
			t.Errorf("结果应为 success，实际 %q", item.Result)
		}
		// 中文名由后端提供，前端不需要维护字典。
		if item.ActionLabel != "创建用户" {
			t.Errorf("中文名应为「创建用户」，实际 %q", item.ActionLabel)
		}
		if item.CreatedAt == 0 {
			t.Error("创建时间必须被填上")
		}
		if item.ID == "" {
			t.Error("ID 必须被填上")
		}
	}
	if !found {
		t.Errorf("未找到 user.create 记录，实际动作: %+v", list.Items)
	}
}

// TestAuditNonAdminSeesOnlyOwn 是本接口最重要的权限测试。
//
// 非 admin **只看自己的操作**。审计记录包含「谁动了哪台服务器、
// 失败原因是什么」，横向可见等于把整个运维拓扑与故障历史
// 摊开给每个登录用户，属于过度暴露。
func TestAuditNonAdminSeesOnlyOwn(t *testing.T) {
	e := newTestEnv(t)
	admin := e.loginAs(t, "admin", testPassword)

	// admin 建一个普通用户，然后该用户自己做一次操作。
	e.createUser(t, admin, "worker", testPassword, "user")
	worker := e.loginAs(t, "worker", testPassword)

	// worker 的操作（改自己的密码之类不方便，用一次会失败的操作也行——
	// 失败也记录，正好验证非 admin 能看到自己的失败记录）。
	_, _ = e.do(t, http.MethodDelete, "/api/users/ghost", "", worker)
	// worker 查列表：只应看到自己的。
	rec, list := e.getAudit(t, "", worker)
	if rec.Code != http.StatusOK {
		t.Fatalf("非 admin 应能查自己的审计，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	for _, item := range list.Items {
		if item.Username == "admin" {
			t.Errorf("非 admin 不应看到别人的记录，却看到了 admin 的: %+v", item)
		}
	}
	// 至少应能看到自己那条。
	if len(list.Items) == 0 {
		t.Error("非 admin 应能看到自己的操作记录")
	}

	// admin 查列表：应该能看到 worker 的。
	_, adminList := e.getAudit(t, "", admin)
	sawWorker := false
	for _, item := range adminList.Items {
		if item.Username == "worker" {
			sawWorker = true
		}
	}
	if !sawWorker {
		t.Error("admin 应能看到所有用户的记录，包括 worker 的")
	}
}

// TestAuditNonAdminCannotForgeUserID 验证非 admin 无法通过参数看别人的。
//
// 这是上一条的强化：即使手工构造 ?user_id=admin 也别想绕过。
// 权限收紧必须发生在服务端参数解析环节，而不是"信任前端不传"。
func TestAuditNonAdminCannotForgeUserID(t *testing.T) {
	e := newTestEnv(t)
	admin := e.loginAs(t, "admin", testPassword)
	e.createUser(t, admin, "worker", testPassword, "user")
	worker := e.loginAs(t, "worker", testPassword)

	// admin 做一个操作产生记录。
	_, _ = e.do(t, http.MethodDelete, "/api/users/ghost", "", admin)

	// worker 尝试用 user_id 参数看 admin 的。
	rec, list := e.getAudit(t, "?user_id=u-test", worker)
	if rec.Code != http.StatusOK {
		t.Fatalf("应返回 200，实际 %d", rec.Code)
	}
	for _, item := range list.Items {
		if item.Username == "admin" {
			t.Errorf("伪造 user_id 不应生效，却看到了 admin 的记录: %+v", item)
		}
	}
}

// TestAuditFilterByAction 验证按动作筛选。
func TestAuditFilterByAction(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	// 产生两种不同动作。
	_, _ = e.do(t, http.MethodPost, "/api/agents/a1/disable", "", cookies)
	_, _ = e.do(t, http.MethodPost, "/api/agents/a1/enable", "", cookies)

	_, list := e.getAudit(t, "?action=agent.disable", cookies)
	if list.Total != 1 {
		t.Fatalf("按动作筛选应得 1 条，实际 %d", list.Total)
	}
	if list.Items[0].Action != "agent.disable" {
		t.Errorf("动作不符: %q", list.Items[0].Action)
	}
	if list.Items[0].ActionLabel != "禁用服务器" {
		t.Errorf("中文名不符: %q", list.Items[0].ActionLabel)
	}
}

// TestAuditFilterByResult 验证按结果筛选。
func TestAuditFilterByResult(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	// 一成功一失败。
	_, _ = e.do(t, http.MethodPost, "/api/agents/a1/disable", "", cookies) // 404，失败
	e.seedAgentRecord(t, "a1", "host-1")
	_, _ = e.do(t, http.MethodPost, "/api/agents/a1/disable", "", cookies) // 成功

	_, failed := e.getAudit(t, "?result=failed", cookies)
	for _, item := range failed.Items {
		if item.Result != "failed" {
			t.Errorf("筛选 failed 却返回了 %q", item.Result)
		}
	}
	if failed.Total == 0 {
		t.Error("应至少有一条失败记录")
	}

	_, ok := e.getAudit(t, "?result=success", cookies)
	for _, item := range ok.Items {
		if item.Result != "success" {
			t.Errorf("筛选 success 却返回了 %q", item.Result)
		}
	}
	if ok.Total == 0 {
		t.Error("应至少有一条成功记录")
	}
}

// TestAuditFilterByAgent 验证按服务器筛选。
func TestAuditFilterByAgent(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")
	e.seedAgentRecord(t, "a2", "host-2")

	_, _ = e.do(t, http.MethodPost, "/api/agents/a1/disable", "", cookies)
	_, _ = e.do(t, http.MethodPost, "/api/agents/a2/disable", "", cookies)

	_, list := e.getAudit(t, "?agent_id=a1", cookies)
	if list.Total != 1 {
		t.Fatalf("按 agent 筛选应得 1 条，实际 %d", list.Total)
	}
	if list.Items[0].AgentID != "a1" {
		t.Errorf("agent_id 不符: %q", list.Items[0].AgentID)
	}
}

// TestAuditPagination 验证分页正确。
func TestAuditPagination(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	// 产生 5 条记录。
	for i := 0; i < 5; i++ {
		_, _ = e.do(t, http.MethodPost, "/api/agents/a1/disable", "", cookies)
		_, _ = e.do(t, http.MethodPost, "/api/agents/a1/enable", "", cookies)
	}

	_, all := e.getAudit(t, "?limit=100", cookies)
	if all.Total < 10 {
		t.Fatalf("应至少 10 条记录，实际 %d", all.Total)
	}

	_, page1 := e.getAudit(t, "?limit=3&offset=0", cookies)
	if len(page1.Items) != 3 {
		t.Errorf("第一页应有 3 条，实际 %d", len(page1.Items))
	}
	// total 必须与分页无关，否则前端分页器永远只有一页。
	if page1.Total != all.Total {
		t.Errorf("total 应恒为 %d（与分页无关），实际 %d", all.Total, page1.Total)
	}

	_, page2 := e.getAudit(t, "?limit=3&offset=3", cookies)
	if len(page2.Items) != 3 {
		t.Errorf("第二页应有 3 条，实际 %d", len(page2.Items))
	}
	// 两页内容不应重叠。
	ids := map[string]bool{}
	for _, item := range page1.Items {
		ids[item.ID] = true
	}
	for _, item := range page2.Items {
		if ids[item.ID] {
			t.Errorf("第二页出现了第一页的记录: %s", item.ID)
		}
	}
}

// TestAuditInvalidParams 验证参数非法时 400。
//
// 关键点：result 非法值必须报错而不是静默返回空列表。
// 空列表会让人以为"那段时间没有失败操作"，实际是自己拼错了参数。
func TestAuditInvalidParams(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	cases := []struct{ name, query string }{
		{"result 非法", "?result=maybe"},
		{"limit 非整数", "?limit=abc"},
		{"offset 非整数", "?offset=abc"},
		{"from 格式错", "?from=not-a-time"},
		{"to 格式错", "?to=not-a-time"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, _ := e.do(t, http.MethodGet, "/api/audit"+c.query, "", cookies)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("应返回 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAuditTimeRangeParams 验证时间参数支持 Unix 秒与 RFC3339。
func TestAuditTimeRangeParams(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	_, _ = e.do(t, http.MethodPost, "/api/agents/a1/disable", "", cookies)

	// 两种格式都应被接受（都不应报 400）。
	for _, q := range []string{
		"?from=1",
		"?from=2020-01-01T00:00:00Z",
		"?to=9999999999",
		"?to=2099-01-01T00:00:00Z",
	} {
		rec, _ := e.do(t, http.MethodGet, "/api/audit"+q, "", cookies)
		if rec.Code != http.StatusOK {
			t.Errorf("%s 应被接受，实际 %d，响应 %s", q, rec.Code, rec.Body.String())
		}
	}
}

// TestAuditRequiresLogin 验证未登录 401。
func TestAuditRequiresLogin(t *testing.T) {
	e := newTestEnv(t)
	rec, _ := e.do(t, http.MethodGet, "/api/audit", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("未登录应返回 401，实际 %d", rec.Code)
	}
}

// TestAuditActionsEndpoint 验证动作清单接口。
//
// 前端靠它渲染筛选下拉框。不给这个接口的话前端只能硬编码一份，
// 后端新增动作时前端就会漏掉，那个动作永远选不出来。
func TestAuditActionsEndpoint(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	rec, _ := e.do(t, http.MethodGet, "/api/audit/actions", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("应返回 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Actions []struct {
			Action string `json:"action"`
			Label  string `json:"label"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(resp.Actions) == 0 {
		t.Fatal("动作清单不应为空")
	}
	for _, a := range resp.Actions {
		if a.Action == "" || a.Label == "" {
			t.Errorf("动作项不完整: %+v", a)
		}
		if a.Action == a.Label {
			t.Errorf("动作 %q 的中文名与标识相同，说明映射缺失", a.Action)
		}
	}
}

// TestAuditDoesNotRecordItsOwnQuery 验证查询审计本身不产生审计记录。
//
// 这是 GET，理应在中间件第一道判断就被放行。
// 若哪天有人在 audit_routes 里误加了 GET 路由，这条会失败——
// 否则"看审计"这个动作会不断产生新审计，形成自我繁殖。
func TestAuditDoesNotRecordItsOwnQuery(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	_, before := e.getAudit(t, "", cookies)
	// 连查 3 次。
	for i := 0; i < 3; i++ {
		e.getAudit(t, "", cookies)
	}
	_, after := e.getAudit(t, "", cookies)

	if after.Total != before.Total {
		t.Errorf("查询审计本身不应产生审计：%d → %d", before.Total, after.Total)
	}
}
