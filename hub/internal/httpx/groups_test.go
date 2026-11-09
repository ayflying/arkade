package httpx

// groups_test.go 的用户组管理接口测试。
//
// 与 admin_test.go 分工：那边测用户，这边测组、成员与授权。
// 两条安全规则（删自己 / 最后一个管理员）只属于用户侧，故不在此文件重复。

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"arkade/hub/internal/store"
)

// createGroup 通过接口创建用户组并返回视图。
func (e *testEnv) createGroup(t *testing.T, cookies []*http.Cookie, name string) groupView {
	t.Helper()
	rec, _ := e.do(t, http.MethodPost, "/api/groups", `{"name":"`+name+`","description":"描述"}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建用户组 %s 应成功，实际 %d，响应 %s", name, rec.Code, rec.Body.String())
	}
	var resp struct {
		Group groupView `json:"group"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析创建组响应失败: %v", err)
	}
	if resp.Group.ID == "" {
		t.Fatalf("创建组未返回 ID: %s", rec.Body.String())
	}
	return resp.Group
}

// listGroups 拉取组列表。
func (e *testEnv) listGroups(t *testing.T, cookies []*http.Cookie) []groupView {
	t.Helper()
	rec, _ := e.do(t, http.MethodGet, "/api/groups", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("列出用户组应返回 200，实际 %d", rec.Code)
	}
	var resp struct {
		Groups []groupView `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析组列表失败: %v", err)
	}
	return resp.Groups
}

// TestCreateGroup 验证创建组的成功路径与名称为空的 400。
func TestCreateGroup(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	grp := env.createGroup(t, cookies, "运维组")
	if grp.Name != "运维组" {
		t.Errorf("组名应为 运维组，实际 %q", grp.Name)
	}
	if grp.MemberCount != 0 || grp.GrantCount != 0 {
		t.Errorf("新组成员数与授权数应为 0，实际 %+v", grp)
	}

	// 名称为空 → 400。
	rec, body := env.do(t, http.MethodPost, "/api/groups", `{"name":"   "}`, cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空组名应返回 400，实际 %d", rec.Code)
	}
	if body.Error.Code != "bad_request" {
		t.Errorf("错误码应为 bad_request，实际 %q", body.Error.Code)
	}
}

// TestCreateGroupDuplicate 验证重名返回 409。
func TestCreateGroupDuplicate(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	env.createGroup(t, cookies, "重名组")

	rec, body := env.do(t, http.MethodPost, "/api/groups", `{"name":"重名组"}`, cookies)
	if rec.Code != http.StatusConflict {
		t.Fatalf("重名组应返回 409，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if body.Error.Code != codeConflict {
		t.Errorf("错误码应为 %q，实际 %q", codeConflict, body.Error.Code)
	}
}

// TestListGroupsWithCounts 验证列表里的成员数与授权数是真实统计值。
func TestListGroupsWithCounts(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	grp := env.createGroup(t, cookies, "统计组")
	idU := env.createUser(t, cookies, "member1", "Password123", "user")

	// 加一个成员、一条授权。
	env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/members", `{"user_ids":["`+idU+`"]}`, cookies)
	env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/grants",
		`{"grants":[{"agent_id":"agent-1","permission":"view"}]}`, cookies)

	groups := env.listGroups(t, cookies)
	if len(groups) != 1 {
		t.Fatalf("应有 1 个组，实际 %d", len(groups))
	}
	if groups[0].MemberCount != 1 {
		t.Errorf("成员数应为 1，实际 %d", groups[0].MemberCount)
	}
	if groups[0].GrantCount != 1 {
		t.Errorf("授权数应为 1，实际 %d", groups[0].GrantCount)
	}
}

// TestUpdateGroup 验证改组名与描述，以及改成已存在的名字返回 409。
func TestUpdateGroup(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	grp := env.createGroup(t, cookies, "旧名字")
	env.createGroup(t, cookies, "已存在的名字")

	rec, _ := env.do(t, http.MethodPatch, "/api/groups/"+grp.ID,
		`{"name":"新名字","description":"新描述"}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("改组应返回 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Group groupView `json:"group"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.Group.Name != "新名字" || resp.Group.Description != "新描述" {
		t.Errorf("修改未生效: %+v", resp.Group)
	}

	// 改成已存在的名字 → 409。
	rec2, body := env.do(t, http.MethodPatch, "/api/groups/"+grp.ID, `{"name":"已存在的名字"}`, cookies)
	if rec2.Code != http.StatusConflict {
		t.Fatalf("改成重名应返回 409，实际 %d", rec2.Code)
	}
	if body.Error.Code != codeConflict {
		t.Errorf("错误码应为 %q，实际 %q", codeConflict, body.Error.Code)
	}

	// 空更新 → 400。
	rec3, _ := env.do(t, http.MethodPatch, "/api/groups/"+grp.ID, `{}`, cookies)
	if rec3.Code != http.StatusBadRequest {
		t.Errorf("空更新应返回 400，实际 %d", rec3.Code)
	}
}

// TestUpdateGroupNotFound 验证改组不存在返回 404。
func TestUpdateGroupNotFound(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodPatch, "/api/groups/g-ghost", `{"name":"x"}`, cookies)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的组应返回 404，实际 %d", rec.Code)
	}
}

// TestDeleteGroup 验证删除组，并确认成员用户本身仍然存在。
func TestDeleteGroup(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	grp := env.createGroup(t, cookies, "待删组")
	idU := env.createUser(t, cookies, "keepme", "Password123", "user")
	env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/members", `{"user_ids":["`+idU+`"]}`, cookies)
	env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/grants",
		`{"grants":[{"agent_id":"agent-1","permission":"operate"}]}`, cookies)

	rec, _ := env.do(t, http.MethodDelete, "/api/groups/"+grp.ID, "", cookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("删除组应返回 204，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	// 组没了。
	if gs := env.listGroups(t, cookies); len(gs) != 0 {
		t.Errorf("删除后不应再有组，实际 %+v", gs)
	}
	// 用户仍在（组只是授权载体，删组不删人）。
	rec2, _ := env.do(t, http.MethodPatch, "/api/users/"+idU, `{"display_name":"还在"}`, cookies)
	if rec2.Code != http.StatusOK {
		t.Errorf("删组不应删除成员用户，实际 %d", rec2.Code)
	}
}

// TestDeleteGroupNotFound 验证删除不存在的组返回 404。
func TestDeleteGroupNotFound(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodDelete, "/api/groups/g-ghost", "", cookies)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的组应返回 404，实际 %d", rec.Code)
	}
}

// TestSetGroupMembers 验证整体设置成员：覆盖旧成员、去重、清空。
func TestSetGroupMembers(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	grp := env.createGroup(t, cookies, "成员组")
	idA := env.createUser(t, cookies, "membera", "Password123", "user")
	idB := env.createUser(t, cookies, "memberb", "Password123", "user")

	// 先设 A。
	rec, _ := env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/members",
		`{"user_ids":["`+idA+`"]}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("设置成员应返回 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	// 整体替换成 B（A 应被移除），并故意重复传 B 两次验证去重。
	rec2, _ := env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/members",
		`{"user_ids":["`+idB+`","`+idB+`"]}`, cookies)
	if rec2.Code != http.StatusOK {
		t.Fatalf("整体替换成员应返回 200，实际 %d，响应 %s", rec2.Code, rec2.Body.String())
	}
	var resp struct {
		Members []groupMemberView `json:"members"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析成员响应失败: %v", err)
	}
	if len(resp.Members) != 1 || resp.Members[0].ID != idB {
		t.Fatalf("成员应只有 B，实际 %+v", resp.Members)
	}

	// 清空成员。
	rec3, _ := env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/members", `{"user_ids":[]}`, cookies)
	if rec3.Code != http.StatusOK {
		t.Fatalf("清空成员应返回 200，实际 %d", rec3.Code)
	}
	rec4, _ := env.do(t, http.MethodGet, "/api/groups/"+grp.ID+"/members", "", cookies)
	var listed struct {
		Members []groupMemberView `json:"members"`
	}
	_ = json.Unmarshal(rec4.Body.Bytes(), &listed)
	if listed.Members == nil {
		t.Error("members 应为空数组而不是 null")
	}
	if len(listed.Members) != 0 {
		t.Errorf("清空后不应有成员，实际 %+v", listed.Members)
	}
}

// TestSetGroupMembersInvalidUser 验证传不存在的用户 ID 返回 400。
func TestSetGroupMembersInvalidUser(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	grp := env.createGroup(t, cookies, "成员组2")

	rec, body := env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/members",
		`{"user_ids":["u-ghost"]}`, cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("不存在的用户应返回 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if !containsSub(body.Error.Message, "u-ghost") {
		t.Errorf("错误文案应指出哪个用户不存在，实际 %q", body.Error.Message)
	}
}

// TestGroupMembersNotFound 验证对不存在的组读写成员返回 404。
func TestGroupMembersNotFound(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodGet, "/api/groups/g-ghost/members", "", cookies)
	if rec.Code != http.StatusNotFound {
		t.Errorf("查不存在组的成员应返回 404，实际 %d", rec.Code)
	}
	rec2, _ := env.do(t, http.MethodPut, "/api/groups/g-ghost/members", `{"user_ids":[]}`, cookies)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("设置不存在组的成员应返回 404，实际 %d", rec2.Code)
	}
}

// TestSetGroupGrants 验证整体设置授权、权限档位字符串、去重与清空。
func TestSetGroupGrants(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	grp := env.createGroup(t, cookies, "授权组")

	body := `{"grants":[{"agent_id":"agent-1","permission":"view"},{"agent_id":"agent-2","permission":"manage"}]}`
	rec, _ := env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/grants", body, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("设置授权应返回 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Grants []groupGrantView `json:"grants"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析授权响应失败: %v", err)
	}
	if len(resp.Grants) != 2 {
		t.Fatalf("应有 2 条授权，实际 %+v", resp.Grants)
	}
	// 按 agent_id 升序返回，顺序稳定。
	if resp.Grants[0].AgentID != "agent-1" || resp.Grants[0].Permission != "view" {
		t.Errorf("第一条授权不符: %+v", resp.Grants[0])
	}
	if resp.Grants[1].Permission != "manage" {
		t.Errorf("第二条授权档位应为 manage，实际 %q", resp.Grants[1].Permission)
	}

	// 整体替换成单条，验证旧授权被清掉。
	rec2, _ := env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/grants",
		`{"grants":[{"agent_id":"agent-3","permission":"operate"}]}`, cookies)
	var resp2 struct {
		Grants []groupGrantView `json:"grants"`
	}
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	if len(resp2.Grants) != 1 || resp2.Grants[0].AgentID != "agent-3" {
		t.Fatalf("整体替换后应只剩 agent-3，实际 %+v", resp2.Grants)
	}

	// 清空。
	rec3, _ := env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/grants", `{"grants":[]}`, cookies)
	var resp3 struct {
		Grants []groupGrantView `json:"grants"`
	}
	_ = json.Unmarshal(rec3.Body.Bytes(), &resp3)
	if resp3.Grants == nil {
		t.Error("grants 应为空数组而不是 null")
	}
	if len(resp3.Grants) != 0 {
		t.Errorf("清空后不应有授权，实际 %+v", resp3.Grants)
	}
}

// TestSetGroupGrantsInvalid 验证非法权限档位与缺 agent_id 返回 400。
func TestSetGroupGrantsInvalid(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	grp := env.createGroup(t, cookies, "授权组2")

	cases := []struct{ name, body, expect string }{
		// "none" 不在允许集合里：要取消授权应该从列表里去掉这条。
		{"权限为 none", `{"grants":[{"agent_id":"a1","permission":"none"}]}`, "permission 必须是"},
		{"权限大小写错", `{"grants":[{"agent_id":"a1","permission":"View"}]}`, "permission 必须是"},
		{"缺 agent_id", `{"grants":[{"permission":"view"}]}`, "缺少 agent_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, body := env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/grants", c.body, cookies)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应返回 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
			if !containsSub(body.Error.Message, c.expect) {
				t.Errorf("错误文案应包含 %q，实际 %q", c.expect, body.Error.Message)
			}
		})
	}
}

// TestGroupGrantsNotFound 验证对不存在的组读写授权返回 404。
func TestGroupGrantsNotFound(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodGet, "/api/groups/g-ghost/grants", "", cookies)
	if rec.Code != http.StatusNotFound {
		t.Errorf("查不存在组的授权应返回 404，实际 %d", rec.Code)
	}
	rec2, _ := env.do(t, http.MethodPut, "/api/groups/g-ghost/grants", `{"grants":[]}`, cookies)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("设置不存在组的授权应返回 404，实际 %d", rec2.Code)
	}
}

// TestGroupGrantTakesEffect 验证组的授权真的会作用于权限判定（端到端）。
//
// 这是本任务最重要的集成断言：管理接口写进去的授权，必须能被
// authz.go 的 loadGrants/canAccess 读到，否则接口只是往表里写字而已。
func TestGroupGrantTakesEffect(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	grp := env.createGroup(t, cookies, "生效组")
	idU := env.createUser(t, cookies, "viewer", "Password123", "user")
	env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/members", `{"user_ids":["`+idU+`"]}`, cookies)

	// 造一台 Agent 记录（只落库，不真连接），让 /api/agents 有可见对象。
	ctx := newTestContext()
	nowUnix := time.Now().UTC().Unix()
	if err := env.db.UpsertAgent(ctx, store.AgentRecord{
		ID: "agent-x", Name: "测试机", Status: "offline",
		Labels: map[string]string{}, CreatedAt: nowUnix, UpdatedAt: nowUnix,
	}); err != nil {
		t.Fatalf("写入测试 Agent 失败: %v", err)
	}

	// 授权前：普通用户看不到任何服务器。
	userCookies := env.loginAs(t, "viewer", "Password123")
	recBefore, _ := env.do(t, http.MethodGet, "/api/agents", "", userCookies)
	var before struct {
		Agents []agentView `json:"agents"`
	}
	_ = json.Unmarshal(recBefore.Body.Bytes(), &before)
	if len(before.Agents) != 0 {
		t.Fatalf("授权前不应看到服务器，实际 %+v", before.Agents)
	}

	// 授予 view 后：应当能看到。
	env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/grants",
		`{"grants":[{"agent_id":"agent-x","permission":"view"}]}`, cookies)
	recAfter, _ := env.do(t, http.MethodGet, "/api/agents", "", userCookies)
	var after struct {
		Agents []agentView `json:"agents"`
	}
	_ = json.Unmarshal(recAfter.Body.Bytes(), &after)
	if len(after.Agents) != 1 || after.Agents[0].ID != "agent-x" {
		t.Fatalf("授权后应能看到 agent-x，实际 %+v", after.Agents)
	}

	// 收回授权后：又看不到了。
	env.do(t, http.MethodPut, "/api/groups/"+grp.ID+"/grants", `{"grants":[]}`, cookies)
	recFinal, _ := env.do(t, http.MethodGet, "/api/agents", "", userCookies)
	var final struct {
		Agents []agentView `json:"agents"`
	}
	_ = json.Unmarshal(recFinal.Body.Bytes(), &final)
	if len(final.Agents) != 0 {
		t.Fatalf("收回授权后不应再看到服务器，实际 %+v", final.Agents)
	}
}
