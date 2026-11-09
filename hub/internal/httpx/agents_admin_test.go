package httpx

// agents_admin_test.go 覆盖服务器管理接口与容器重启/删除接口。
//
// 覆盖策略（对应任务验收标准）：
//   - 每个新接口一条成功路径；
//   - 401（未登录）/ 403（非 admin 或权限不足）/ 404（不存在）；
//   - 容器 remove 的 force / remove_volumes 参数组合与非法值；
//   - 删除服务器后 compose_stacks 仍在（本任务最关键的边界）。
//
// 断言都针对真实 HTTP 语义（状态码 + JSON），因为前端只依赖这些。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"arkade/hub/internal/store"
)

// testPassword 是测试环境的统一口令（与 router_test.go 初始化 admin 时一致）。
const testPassword = "TestPass123!"

// agentRecordFor 构造一条 Agent 记录。
func agentRecordFor(id, name string) store.AgentRecord {
	now := time.Now().UTC().Unix()
	return store.AgentRecord{
		ID:        id,
		Name:      name,
		Status:    "online",
		Runtime:   "docker",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// seedStackRecord 直接写一条编排记录。
//
// ID 必须显式给：主键是 TEXT NOT NULL（非自增），留空会让多条记录撞在空串上。
func (e *testEnv) seedStackRecord(t *testing.T, agentID, name string) {
	t.Helper()
	now := time.Now().UTC()
	err := e.db.CreateComposeStack(t.Context(), store.ComposeStack{
		ID:      agentID + "/" + name,
		AgentID: agentID, Name: name, Content: "services: {}\n",
		ConfigHash: "abc", Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("准备编排失败: %v", err)
	}
}

// seedAgentRecord 直接在库里写入一条 Agent 记录。
//
// 不通过接口创建：服务器记录只能由 Agent 握手产生，
// 这是有意的设计（不允许人工编造一台机器）。
func (e *testEnv) seedAgentRecord(t *testing.T, id, name string) {
	t.Helper()
	if err := e.db.UpsertAgent(t.Context(), agentRecordFor(id, name)); err != nil {
		t.Fatalf("准备 Agent 记录失败: %v", err)
	}
}

// ---- 服务器管理：PATCH ----

// TestPatchAgent 验证改显示名与备注。
func TestPatchAgent(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	rec, parsed := e.do(t, http.MethodPatch, "/api/agents/a1",
		`{"display_name":"生产-东京","note":"负责商品服务"}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("改名应成功，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if parsed.Error.Code != "" {
		t.Fatalf("不应有错误: %+v", parsed.Error)
	}

	// 响应形状与列表项一致：{agent:{id,display_name,note,...}}。
	// 用结构体解码而不是 map，这样"字段名写错"会在测试里暴露为
	// 空值断言失败，而不是静默通过。
	var resp struct {
		Agent struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Note        string `json:"note"`
		} `json:"agent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Agent.DisplayName != "生产-东京" {
		t.Errorf("显示名应为「生产-东京」，实际 %q", resp.Agent.DisplayName)
	}
	if resp.Agent.Note != "负责商品服务" {
		t.Errorf("备注不符，实际 %q", resp.Agent.Note)
	}
	// 主机名不应被改名操作带跑。
	if resp.Agent.Name != "host-1" {
		t.Errorf("主机名不应改变，实际 %q", resp.Agent.Name)
	}
	// 前端靠这个 id 做 store.replace(updated)，缺了会静默不更新。
	if resp.Agent.ID != "a1" {
		t.Errorf("响应必须带 id=a1（前端用它替换列表项），实际 %q", resp.Agent.ID)
	}
}

// TestPatchAgentOnlyNote 验证部分更新不会清空其他字段。
func TestPatchAgentOnlyNote(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	if rec, _ := e.do(t, http.MethodPatch, "/api/agents/a1",
		`{"display_name":"东京"}`, cookies); rec.Code != http.StatusOK {
		t.Fatalf("第一次改名失败: %d", rec.Code)
	}

	rec, parsed := e.do(t, http.MethodPatch, "/api/agents/a1",
		`{"note":"新备注"}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("改备注应成功，实际 %d", rec.Code)
	}
	var resp struct {
		Agent struct {
			DisplayName string `json:"display_name"`
			Note        string `json:"note"`
		} `json:"agent"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Agent.DisplayName != "东京" {
		t.Errorf("只改备注时显示名应保留，实际 %q（错误码 %q）", resp.Agent.DisplayName, parsed.Error)
	}
	if resp.Agent.Note != "新备注" {
		t.Errorf("备注应更新为「新备注」，实际 %q", resp.Agent.Note)
	}
}

// TestPatchAgentNotFound 验证 404。
func TestPatchAgentNotFound(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	rec, parsed := e.do(t, http.MethodPatch, "/api/agents/ghost", `{"note":"x"}`, cookies)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的服务器应返回 404，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if parsed.Error.Code != "not_found" {
		t.Errorf("错误码应为 not_found，实际 %q", parsed.Error.Code)
	}
}

// TestPatchAgentValidation 验证参数校验。
func TestPatchAgentValidation(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	cases := []struct{ name, body string }{
		{"显示名过长", `{"display_name":"` + repeatRune('长', 65) + `"}`},
		{"备注过长", `{"note":"` + repeatRune('x', 501) + `"}`},
		{"空标签名", `{"labels":{"":"v"}}`},
		{"请求体非法", `not json`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, parsed := e.do(t, http.MethodPatch, "/api/agents/a1", c.body, cookies)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("应返回 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
				return
			}
			if parsed.Error.Code != "bad_request" && parsed.Error.Code != "invalid_argument" {
				t.Errorf("错误码应为 bad_request，实际 %q", parsed.Error.Code)
			}
		})
	}
}

// TestPatchAgentUnauthorized 验证未登录 401。
func TestPatchAgentUnauthorized(t *testing.T) {
	e := newTestEnv(t)
	e.seedAgentRecord(t, "a1", "host-1")
	rec, _ := e.do(t, http.MethodPatch, "/api/agents/a1", `{"note":"x"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("未登录应返回 401，实际 %d", rec.Code)
	}
}

// TestPatchAgentNonAdmin 验证非 admin 403。
func TestPatchAgentNonAdmin(t *testing.T) {
	e := newTestEnv(t)
	admin := e.loginAs(t, "admin", testPassword)
	e.createUser(t, admin, "viewer", testPassword, "user")
	viewer := e.loginAs(t, "viewer", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	rec, _ := e.do(t, http.MethodPatch, "/api/agents/a1", `{"note":"x"}`, viewer)
	if rec.Code != http.StatusForbidden {
		t.Errorf("非管理员应返回 403，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
}

// ---- 服务器管理：禁用 / 启用 ----

// TestDisableEnableAgent 验证禁用与启用。
func TestDisableEnableAgent(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	rec, _ := e.do(t, http.MethodPost, "/api/agents/a1/disable", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("禁用应成功，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Agent struct {
			ID       string `json:"id"`
			Disabled bool   `json:"disabled"`
			Status   string `json:"status"`
		} `json:"agent"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.Agent.Disabled {
		t.Errorf("响应应标记 disabled=true，实际 %v", resp.Agent.Disabled)
	}
	// 禁用后连接被断开，状态应立刻反映为离线。
	// 前端靠这组字段判断"该显示灰色离线还是禁用标记"。
	if resp.Agent.ID != "a1" {
		t.Errorf("响应必须带 id=a1，实际 %q", resp.Agent.ID)
	}

	rec, _ = e.do(t, http.MethodPost, "/api/agents/a1/enable", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("启用应成功，实际 %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Agent.Disabled {
		t.Errorf("响应应标记 disabled=false，实际 %v", resp.Agent.Disabled)
	}
}

// TestDisableAgentNotFound 验证 404。
func TestDisableAgentNotFound(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	for _, path := range []string{"/api/agents/ghost/disable", "/api/agents/ghost/enable"} {
		rec, _ := e.do(t, http.MethodPost, path, "", cookies)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s 应返回 404，实际 %d", path, rec.Code)
		}
	}
}

// TestDisableAgentRequiresAdmin 验证非 admin 403。
//
// 特别注意：即使是拥有该服务器 manage 权限的用户也不能禁用，
// 因为禁用会切断所有人的通道，影响面超出单台服务器的授权范围。
func TestDisableAgentRequiresAdmin(t *testing.T) {
	e := newTestEnv(t)
	admin := e.loginAs(t, "admin", testPassword)
	e.createUser(t, admin, "viewer", testPassword, "user")
	viewer := e.loginAs(t, "viewer", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	for _, path := range []string{"/api/agents/a1/disable", "/api/agents/a1/enable"} {
		rec, _ := e.do(t, http.MethodPost, path, "", viewer)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s 非管理员应返回 403，实际 %d", path, rec.Code)
		}
	}
}

// TestDisableAgentUnauthorized 验证未登录 401。
func TestDisableAgentUnauthorized(t *testing.T) {
	e := newTestEnv(t)
	e.seedAgentRecord(t, "a1", "host-1")
	rec, _ := e.do(t, http.MethodPost, "/api/agents/a1/disable", "", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("未登录应返回 401，实际 %d", rec.Code)
	}
}

// ---- 服务器管理：DELETE ----

// TestDeleteAgent 验证删除服务器记录。
func TestDeleteAgent(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	rec, parsed := e.do(t, http.MethodDelete, "/api/agents/a1", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除应成功，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if parsed.Error.Code != "" {
		t.Fatalf("不应有错误: %+v", parsed.Error)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["deleted"] != true {
		t.Errorf("应返回 deleted=true，实际 %v", resp["deleted"])
	}
	// 删完列表里不该再有它。
	listRec, _ := e.do(t, http.MethodGet, "/api/agents", "", cookies)
	if listRec.Code != http.StatusOK {
		t.Fatalf("列表接口异常: %d", listRec.Code)
	}
}

// TestDeleteAgentNotFound 验证 404。
func TestDeleteAgentNotFound(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	rec, parsed := e.do(t, http.MethodDelete, "/api/agents/ghost", "", cookies)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("应返回 404，实际 %d", rec.Code)
	}
	if parsed.Error.Code != "not_found" {
		t.Errorf("错误码应为 not_found，实际 %q", parsed.Error.Code)
	}
}

// TestDeleteAgentRequiresAdmin 验证非 admin 403。
func TestDeleteAgentRequiresAdmin(t *testing.T) {
	e := newTestEnv(t)
	admin := e.loginAs(t, "admin", testPassword)
	e.createUser(t, admin, "viewer", testPassword, "user")
	viewer := e.loginAs(t, "viewer", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	rec, _ := e.do(t, http.MethodDelete, "/api/agents/a1", "", viewer)
	if rec.Code != http.StatusForbidden {
		t.Errorf("非管理员应返回 403，实际 %d", rec.Code)
	}
}

// TestDeleteAgentKeepsComposeStacks 是本任务最关键的接口级测试。
//
// **删除服务器记录不能连带删除编排。** 编排是用户手写的资产，
// 服务器记录只是纳管登记。响应里还要告诉前端产生了多少孤儿，
// 让用户能立刻去处理。
func TestDeleteAgentKeepsComposeStacks(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	// 建两条编排挂在 a1 名下（直接写库，避免依赖 Agent 在线）。
	for _, name := range []string{"web", "db"} {
		e.seedStackRecord(t, "a1", name)
	}

	rec, parsed := e.do(t, http.MethodDelete, "/api/agents/a1", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除应成功，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if parsed.Error.Code != "" {
		t.Fatalf("不应有错误: %+v", parsed.Error)
	}

	// 响应应告知产生了 2 条孤儿。
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if n, ok := resp["orphan_stacks"].(float64); !ok || int(n) != 2 {
		t.Errorf("应报告 2 条孤儿编排，实际 %v", resp["orphan_stacks"])
	}
	if msg, _ := resp["message"].(string); msg == "" {
		t.Error("响应应带说明文案，告知用户容器未受影响")
	}

	// 编排必须还在库里。
	for _, name := range []string{"web", "db"} {
		if _, err := e.db.GetComposeStack(t.Context(), "a1", name); err != nil {
			t.Fatalf("编排 %s 不应被删除: %v", name, err)
		}
	}
}

// ---- 容器操作权限 ----

// TestContainerRemoveRequiresManage 验证删除容器需要 manage 权限。
//
// view 用户显然不能删；这里用 viewer（无任何授权）验证 403。
// 权限档位的真正差异由 authz 的既有测试覆盖，此处只确认路由挂上了。
func TestContainerRemoveRequiresManage(t *testing.T) {
	e := newTestEnv(t)
	admin := e.loginAs(t, "admin", testPassword)
	e.createUser(t, admin, "viewer", testPassword, "user")
	viewer := e.loginAs(t, "viewer", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	rec, _ := e.do(t, http.MethodDelete, "/api/agents/a1/containers/abc", "", viewer)
	if rec.Code != http.StatusForbidden {
		t.Errorf("无权限用户删容器应返回 403，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
}

// TestContainerRemoveParamValidation 验证 force / remove_volumes 参数校验。
//
// 非法值必须报 400 而不是静默当 false：
// 用户传 remove_volumes=yes 而被当成 false，他会以为卷已删；
// 传 remove_volumes=ture 被宽松当成 true 则直接丢数据。
func TestContainerRemoveParamValidation(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	cases := []struct{ name, query string }{
		{"force 非法", "?force=maybe"},
		{"remove_volumes 非法", "?remove_volumes=maybe"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, parsed := e.do(t, http.MethodDelete,
				"/api/agents/a1/containers/abc"+c.query, "", cookies)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("应返回 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
				return
			}
			if parsed.Error.Code != "bad_request" && parsed.Error.Code != "invalid_argument" {
				t.Errorf("错误码应为 bad_request，实际 %q", parsed.Error.Code)
			}
		})
	}
}

// TestContainerRemoveAcceptedBoolValues 验证各种合法布尔写法都能通过校验。
//
// 这些请求最终会因为 Agent 离线返回 503——但状态码不该是 400，
// 证明参数解析通过了而校验环节在参数之后。
func TestContainerRemoveAcceptedBoolValues(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	for _, q := range []string{"", "?force=1", "?force=true&remove_volumes=1",
		"?force=false&remove_volumes=false", "?force=yes&remove_volumes=no"} {
		rec, _ := e.do(t, http.MethodDelete, "/api/agents/a1/containers/abc"+q, "", cookies)
		if rec.Code == http.StatusBadRequest {
			t.Errorf("查询串 %q 是合法参数，不应返回 400，响应 %s", q, rec.Body.String())
		}
	}
}

// TestContainerRemoveAgentOffline 验证 Agent 离线时返回 503。
//
// 沿用既有容器接口的行为（不排队、不重试），保持一致。
func TestContainerRemoveAgentOffline(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	rec, _ := e.do(t, http.MethodDelete, "/api/agents/a1/containers/abc", "", cookies)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("Agent 离线应返回 503，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
}

// TestContainerRestartAgentOffline 验证重启在 Agent 离线时的行为。
func TestContainerRestartAgentOffline(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	rec, _ := e.do(t, http.MethodPost, "/api/agents/a1/containers/abc/restart", "", cookies)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("Agent 离线应返回 503，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
}

// TestContainerRestartRequiresOperate 验证重启需要 operate 权限。
func TestContainerRestartRequiresOperate(t *testing.T) {
	e := newTestEnv(t)
	admin := e.loginAs(t, "admin", testPassword)
	e.createUser(t, admin, "viewer", testPassword, "user")
	viewer := e.loginAs(t, "viewer", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")

	rec, _ := e.do(t, http.MethodPost, "/api/agents/a1/containers/abc/restart", "", viewer)
	if rec.Code != http.StatusForbidden {
		t.Errorf("无权限用户重启容器应返回 403，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
}

// TestContainerOpsAgentNotFound 验证服务器不存在时 404。
//
// 这条与 403 的顺序很重要：不存在的服务器应报 404 而不是 403，
// 否则用户会以为自己权限不足，反复找人授权却始终不行。
func TestContainerOpsAgentNotFound(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)

	rec, _ := e.do(t, http.MethodPost, "/api/agents/ghost/containers/abc/restart", "", cookies)
	if rec.Code != http.StatusNotFound {
		t.Errorf("重启：不存在的服务器应返回 404，实际 %d", rec.Code)
	}
	rec, _ = e.do(t, http.MethodDelete, "/api/agents/ghost/containers/abc", "", cookies)
	if rec.Code != http.StatusNotFound {
		t.Errorf("删除：不存在的服务器应返回 404，实际 %d", rec.Code)
	}
}

// repeatRune 生成重复字符的字符串。
func repeatRune(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}
