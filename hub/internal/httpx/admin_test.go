package httpx

// admin_test.go 的用户与用户组管理接口测试。
//
// 覆盖策略（对应任务验收标准）：
//   - 每个接口至少一条成功路径；
//   - 每条接口都覆盖 401（未登录）/ 403（非 admin）；
//   - 参数非法 400、记录不存在 404、重名 409；
//   - 两条安全规则单独成测：删除自己被拒绝、最后一个管理员不能降级/禁用/删除。
//
// 断言一律针对真实 HTTP 语义（状态码 + JSON 结构），因为前端只依赖这些。

import (
	"encoding/json"
	"net/http"
	"testing"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// createUser 通过接口创建一个用户并返回其 ID。失败直接终止测试。
func (e *testEnv) createUser(t *testing.T, cookies []*http.Cookie, username, password, role string) string {
	t.Helper()
	body := `{"username":"` + username + `","display_name":"` + username + `","password":"` + password + `","role":"` + role + `"}`
	rec, parsed := e.do(t, http.MethodPost, "/api/users", body, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建用户 %s 应成功，实际 %d，响应 %s", username, rec.Code, rec.Body.String())
	}
	var resp struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析创建用户响应失败: %v", err)
	}
	if resp.User.ID == "" {
		t.Fatalf("创建用户未返回 ID，响应 %s（错误码 %q）", rec.Body.String(), parsed.Error.Code)
	}
	return resp.User.ID
}

// loginAs 用指定账号登录并返回会话 Cookie。
func (e *testEnv) loginAs(t *testing.T, username, password string) []*http.Cookie {
	t.Helper()
	rec, _ := e.do(t, http.MethodPost, "/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("账号 %s 登录应成功，实际 %d，响应 %s", username, rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	var login struct {
		User struct {
			MustChangePassword bool `json:"must_change_password"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if login.User.MustChangePassword {
		// This legacy helper represents an established usable account. Exercise
		// the real first-change flow instead of clearing the flag in storage.
		// Dedicated forced-change tests use raw HTTP rather than this helper.
		replacement := password + "!Changed"
		changed := e.completeInitialPassword(t, username, password, replacement, cookies)
		// Restore the fixture's known password through a second legitimate
		// change, preserving callers that subsequently test that credential.
		return e.completeInitialPassword(t, username, replacement, password, changed)
	}
	return cookies
}

// completeInitialPassword follows the real mandatory-change flow, including
// session revocation and an explicit login with the replacement password.
func (e *testEnv) completeInitialPassword(t *testing.T, username, oldPassword, newPassword string, cookies []*http.Cookie) []*http.Cookie {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"old_password": oldPassword, "new_password": newPassword})
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := e.do(t, http.MethodPut, "/api/account/password", string(payload), cookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("首次改密应返回204，实际%d：%s", rec.Code, rec.Body.String())
	}
	return e.loginAs(t, username, newPassword)
}

// decodeUsers 解析用户列表响应。
type usersResponse struct {
	Users []userAdminView `json:"users"`
}

// ---- 用户接口 ----

// TestListUsers 验证列表接口返回用户与所属组，且绝不包含密码哈希。
func TestListUsers(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodGet, "/api/users", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("列出用户应返回 200，实际 %d", rec.Code)
	}
	// 密码哈希绝不能出现在响应里——这是最硬的安全约束，直接查原始响应体。
	if body := rec.Body.String(); containsSub(body, "password_hash") || containsSub(body, "$2a$") {
		t.Fatalf("响应中出现了密码哈希相关内容: %s", body)
	}

	var resp usersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析用户列表失败: %v", err)
	}
	if len(resp.Users) != 1 || resp.Users[0].Username != "admin" {
		t.Fatalf("应有 1 个 admin 用户，实际 %+v", resp.Users)
	}
	// group_ids 必须是空数组而不是 null，前端才能无条件遍历。
	if resp.Users[0].GroupIDs == nil {
		t.Error("group_ids 应为空数组而不是 null")
	}
}

// TestCreateUser 验证创建用户的成功路径与用户名规整。
//
// 重点：用户名传 "  NEWUSER  " 应被规整为 "newuser"（复用 user.NormalizeUsername），
// 且用规整后的名字能登录——证明创建与登录用的是同一套规整规则。
func TestCreateUser(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, body := env.do(t, http.MethodPost, "/api/users",
		`{"username":"  NEWUSER  ","display_name":"新用户","password":"Password123!","role":"user"}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("创建用户应返回 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	_ = body

	var resp struct {
		User userAdminView `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.User.Username != "newuser" {
		t.Errorf("用户名应被规整为 newuser，实际 %q", resp.User.Username)
	}
	if resp.User.Role != "user" {
		t.Errorf("角色应为 user，实际 %q", resp.User.Role)
	}

	// 用规整后的用户名可以登录，说明创建与登录的规整口径一致。
	env.loginAs(t, "newuser", "Password123!")
}

// TestCreateUserDuplicate 验证重名返回 409 conflict。
func TestCreateUserDuplicate(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	// 已存在 admin（大小写不同也算重复，因为规整后相同）。
	rec, body := env.do(t, http.MethodPost, "/api/users",
		`{"username":"ADMIN","display_name":"x","password":"Password123!","role":"user"}`, cookies)
	if rec.Code != http.StatusConflict {
		t.Fatalf("重名应返回 409，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if body.Error.Code != codeConflict {
		t.Errorf("错误码应为 %q，实际 %q", codeConflict, body.Error.Code)
	}
}

// TestCreateUserInvalid 验证参数校验：短密码、非法角色、非法用户名。
func TestCreateUserInvalid(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	cases := []struct {
		name   string
		body   string
		expect string
	}{
		// 用户名必须合法，才能让用例真正测到「密码」「角色」的校验分支：
		// 用户名先被拦下会让断言指向错误的字段。
		{"密码太短", `{"username":"user1","password":"short","role":"user"}`, "密码至少"},
		{"非法角色", `{"username":"user2","password":"Password123!","role":"Admin"}`, "角色只能是"},
		{"用户名带空格", `{"username":"a b","password":"Password123!","role":"user"}`, "用户名只能包含"},
		{"用户名全数字", `{"username":"12345","password":"Password123!","role":"user"}`, "不能全为数字"},
		{"未知字段", `{"username":"user3","password":"Password123!","role":"user","x":1}`, "请求格式错误"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, body := env.do(t, http.MethodPost, "/api/users", c.body, cookies)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应返回 400，实际 %d，响应 %s", rec.Code, rec.Body.String())
			}
			if !containsSub(body.Error.Message, c.expect) {
				t.Errorf("错误文案应包含 %q，实际 %q", c.expect, body.Error.Message)
			}
		})
	}
}

// TestUpdateUser 验证修改显示名、角色、禁用状态。
func TestUpdateUser(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	id := env.createUser(t, cookies, "target", "Password123!", "user")

	rec, _ := env.do(t, http.MethodPatch, "/api/users/"+id,
		`{"display_name":"改过的名字","role":"admin"}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("修改用户应返回 200，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		User userAdminView `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if resp.User.DisplayName != "改过的名字" || resp.User.Role != "admin" {
		t.Errorf("修改未生效: %+v", resp.User)
	}

	// 禁用该用户后，其会话应立即失效（Authenticate 会检查 Disabled）。
	targetCookies := env.loginAs(t, "target", "Password123!")
	rec2, _ := env.do(t, http.MethodPatch, "/api/users/"+id, `{"disabled":true}`, cookies)
	if rec2.Code != http.StatusOK {
		t.Fatalf("禁用用户应返回 200，实际 %d", rec2.Code)
	}
	rec3, _ := env.do(t, http.MethodGet, "/api/auth/me", "", targetCookies)
	if rec3.Code != http.StatusUnauthorized {
		t.Fatalf("被禁用用户的会话应失效（401），实际 %d", rec3.Code)
	}
}

// TestUpdateUserNotFound 验证修改不存在的用户返回 404。
func TestUpdateUserNotFound(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, body := env.do(t, http.MethodPatch, "/api/users/u-ghost", `{"display_name":"x"}`, cookies)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的用户应返回 404，实际 %d", rec.Code)
	}
	if body.Error.Code != "not_found" {
		t.Errorf("错误码应为 not_found，实际 %q", body.Error.Code)
	}
}

// TestUpdateUserNoFields 验证空 PATCH 返回 400（而不是静默成功）。
func TestUpdateUserNoFields(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodPatch, "/api/users/u-test", `{}`, cookies)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("空更新应返回 400，实际 %d", rec.Code)
	}
}

// TestResetPassword 验证重置密码：新密码可登录，旧密码失效，旧会话被清除。
func TestResetPassword(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	id := env.createUser(t, cookies, "victim", "Password123!", "user")

	oldCookies := env.loginAs(t, "victim", "Password123!")

	rec, _ := env.do(t, http.MethodPut, "/api/users/"+id+"/password", `{"password":"NewPassword456"}`, cookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("重置密码应返回 204，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	// 旧会话必须失效。
	rec2, _ := env.do(t, http.MethodGet, "/api/auth/me", "", oldCookies)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("改密后旧会话应失效，实际 %d", rec2.Code)
	}
	// 旧密码不可登录，新密码可以。
	rec3, _ := env.do(t, http.MethodPost, "/api/auth/login",
		`{"username":"victim","password":"Password123!"}`, nil)
	if rec3.Code != http.StatusUnauthorized {
		t.Errorf("旧密码应失效，实际 %d", rec3.Code)
	}
	env.loginAs(t, "victim", "NewPassword456")
}

// TestDeleteUser 验证删除用户成功，且关联的会话与组关系被清理。
func TestDeleteUser(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	id := env.createUser(t, cookies, "doomed", "Password123!", "user")

	// 先给他建一个组并加入，验证删除后组成员关系被清理。
	recG, _ := env.do(t, http.MethodPost, "/api/groups", `{"name":"清理测试组"}`, cookies)
	var grpResp struct {
		Group groupView `json:"group"`
	}
	if err := json.Unmarshal(recG.Body.Bytes(), &grpResp); err != nil {
		t.Fatalf("解析创建组响应失败: %v", err)
	}
	env.do(t, http.MethodPut, "/api/groups/"+grpResp.Group.ID+"/members",
		`{"user_ids":["`+id+`"]}`, cookies)

	rec, _ := env.do(t, http.MethodDelete, "/api/users/"+id, "", cookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("删除用户应返回 204，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	// 用户已不存在。
	rec2, _ := env.do(t, http.MethodPatch, "/api/users/"+id, `{"display_name":"x"}`, cookies)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("删除后再访问应 404，实际 %d", rec2.Code)
	}
	// 组成员关系也已清理。
	rec3, _ := env.do(t, http.MethodGet, "/api/groups/"+grpResp.Group.ID+"/members", "", cookies)
	var members struct {
		Members []groupMemberView `json:"members"`
	}
	if err := json.Unmarshal(rec3.Body.Bytes(), &members); err != nil {
		t.Fatalf("解析成员列表失败: %v", err)
	}
	if len(members.Members) != 0 {
		t.Errorf("删除用户后组内不应有成员，实际 %+v", members.Members)
	}
}

// TestDeleteUserNotFound 验证删除不存在的用户返回 404。
func TestDeleteUserNotFound(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodDelete, "/api/users/u-ghost", "", cookies)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的用户应返回 404，实际 %d", rec.Code)
	}
}

// ---- 安全规则（必须实现且必须测）----

// TestCannotDeleteSelf 验证管理员不能删除自己。
func TestCannotDeleteSelf(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, body := env.do(t, http.MethodDelete, "/api/users/u-test", "", cookies)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("删除自己应返回 403，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if body.Error.Code != "forbidden" {
		t.Errorf("错误码应为 forbidden，实际 %q", body.Error.Code)
	}

	// 自己必须还在，且会话仍然可用。
	rec2, _ := env.do(t, http.MethodGet, "/api/auth/me", "", cookies)
	if rec2.Code != http.StatusOK {
		t.Errorf("被拒绝后自己的会话应仍然有效，实际 %d", rec2.Code)
	}
}

// TestCannotDemoteLastAdmin 验证唯一管理员不能被降级。
func TestCannotDemoteLastAdmin(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, body := env.do(t, http.MethodPatch, "/api/users/u-test", `{"role":"user"}`, cookies)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("降级唯一管理员应返回 403，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if body.Error.Code != "forbidden" {
		t.Errorf("错误码应为 forbidden，实际 %q", body.Error.Code)
	}

	// 角色必须没变。
	u, err := env.db.GetUserByID(newTestContext(), "u-test")
	if err != nil {
		t.Fatalf("查询用户失败: %v", err)
	}
	if u.Role != user.RoleAdmin {
		t.Errorf("被拒绝后角色应仍为 admin，实际 %q", u.Role)
	}
}

// TestCannotDisableLastAdmin 验证唯一管理员不能被禁用。
func TestCannotDisableLastAdmin(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, body := env.do(t, http.MethodPatch, "/api/users/u-test", `{"disabled":true}`, cookies)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("禁用唯一管理员应返回 403，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
	if body.Error.Code != "forbidden" {
		t.Errorf("错误码应为 forbidden，实际 %q", body.Error.Code)
	}
}

// TestCannotDeleteLastAdmin 验证唯一管理员不能被删除（由另一个管理员发起）。
//
// 构造方式：先把 admin 之外再建一个管理员 B，用 B 的会话删除 admin。
// 此时系统里还有 B，所以删除 admin 应当成功——这反过来说明：
// 「不能删除最后一个管理员」真正限制的是「删完之后还剩 0 个」这个状态。
// 因此本测试同时验证两个方向：
//  1. 存在另一个管理员时，可以删除其中一个（否则规则会过度限制）；
//  2. 删除后只剩的那个，不能再被任何方式移除。
func TestCannotDeleteLastAdmin(t *testing.T) {
	env := newTestEnv(t)
	adminCookies := env.login(t)

	// 造一个「唯一管理员」的场景：把 admin 降级失败过，这里反过来——
	// 先建第二个管理员 B，然后让 B 删 admin。
	idB := env.createUser(t, adminCookies, "adminb", "Password123!", "admin")
	bCookies := env.loginAs(t, "adminb", "Password123!")

	// B 删 admin：这时还剩 B，属于允许的操作。
	rec, _ := env.do(t, http.MethodDelete, "/api/users/u-test", "", bCookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("存在另一个管理员时应允许删除，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	// 现在 B 是唯一管理员：B 不能降级自己、不能禁用自己。
	rec2, _ := env.do(t, http.MethodPatch, "/api/users/"+idB, `{"role":"user"}`, bCookies)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("唯一管理员降级自己应被拒绝，实际 %d", rec2.Code)
	}
	rec3, _ := env.do(t, http.MethodPatch, "/api/users/"+idB, `{"disabled":true}`, bCookies)
	if rec3.Code != http.StatusForbidden {
		t.Errorf("唯一管理员禁用自己应被拒绝，实际 %d", rec3.Code)
	}
	// 删自己已被「不能删自己」拦住，这里确认两条规则叠加后的结果仍是 403。
	rec4, _ := env.do(t, http.MethodDelete, "/api/users/"+idB, "", bCookies)
	if rec4.Code != http.StatusForbidden {
		t.Errorf("唯一管理员删除自己应被拒绝，实际 %d", rec4.Code)
	}
}

// TestLastAdminAllowsWhenOthersExist 验证有多个管理员时可以降级其中一个。
//
// 这条测试防的是「保护规则写得过宽」——如果实现成「只要是管理员就不许降级」，
// 运维就永远无法轮换管理员。
func TestLastAdminAllowsWhenOthersExist(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	idB := env.createUser(t, cookies, "adminb", "Password123!", "admin")

	rec, _ := env.do(t, http.MethodPatch, "/api/users/"+idB, `{"role":"user"}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("还有其他管理员时应允许降级，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	// 降级后系统里只剩 admin 一个有效管理员，此时再降级自己应当被拒绝。
	rec2, _ := env.do(t, http.MethodPatch, "/api/users/u-test", `{"role":"user"}`, cookies)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("降级后唯一管理员再降级应被拒绝，实际 %d", rec2.Code)
	}
}

// TestDisabledAdminNotCounted 验证被禁用的管理员不计入「有效管理员」。
//
// 场景：admin A 与 admin B，把 B 禁用；此后 A 就是唯一有效管理员，
// 不能降级自己（否则系统里没有任何人能登录管理）。这是「有效」二字的意义。
func TestDisabledAdminNotCounted(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)
	idB := env.createUser(t, cookies, "adminb", "Password123!", "admin")

	// 禁用 B（此时 A 仍有效，所以允许）。
	rec, _ := env.do(t, http.MethodPatch, "/api/users/"+idB, `{"disabled":true}`, cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("禁用另一个管理员应允许，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}

	// 现在 A 是唯一有效管理员，不能降级自己。
	rec2, _ := env.do(t, http.MethodPatch, "/api/users/u-test", `{"role":"user"}`, cookies)
	if rec2.Code != http.StatusForbidden {
		t.Errorf("被禁用的管理员不应计入有效管理员，应拒绝降级，实际 %d", rec2.Code)
	}
}

// ---- 非 admin 访问控制 ----

// TestNonAdminForbidden 验证普通用户访问全部管理接口都是 403。
func TestNonAdminForbidden(t *testing.T) {
	env := newTestEnv(t)
	adminCookies := env.login(t)
	env.createUser(t, adminCookies, "normal", "Password123!", "user")
	userCookies := env.loginAs(t, "normal", "Password123!")

	// 普通用户即使拥有 manage 档的服务器授权，也不能碰用户与组（权限来源不能自己改）。
	endpoints := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/api/users", ""},
		{http.MethodPost, "/api/users", `{"username":"x1","password":"Password123!","role":"user"}`},
		{http.MethodPatch, "/api/users/u-test", `{"display_name":"x"}`},
		{http.MethodPut, "/api/users/u-test/password", `{"password":"Password123!"}`},
		{http.MethodDelete, "/api/users/u-test", ""},
		{http.MethodGet, "/api/groups", ""},
		{http.MethodPost, "/api/groups", `{"name":"x"}`},
		{http.MethodPatch, "/api/groups/g-1", `{"name":"x"}`},
		{http.MethodDelete, "/api/groups/g-1", ""},
		{http.MethodGet, "/api/groups/g-1/members", ""},
		{http.MethodPut, "/api/groups/g-1/members", `{"user_ids":[]}`},
		{http.MethodGet, "/api/groups/g-1/grants", ""},
		{http.MethodPut, "/api/groups/g-1/grants", `{"grants":[]}`},
	}
	for _, ep := range endpoints {
		rec, body := env.do(t, ep.method, ep.path, ep.body, userCookies)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s 非管理员应返回 403，实际 %d", ep.method, ep.path, rec.Code)
			continue
		}
		if body.Error.Code != "forbidden" {
			t.Errorf("%s %s 错误码应为 forbidden，实际 %q", ep.method, ep.path, body.Error.Code)
		}
	}
}

// TestAdminEndpointsRequire401 验证全部管理接口未登录时都是 401。
func TestAdminEndpointsRequire401(t *testing.T) {
	env := newTestEnv(t)
	endpoints := []struct{ method, path string }{
		{http.MethodGet, "/api/users"},
		{http.MethodPost, "/api/users"},
		{http.MethodPatch, "/api/users/u-test"},
		{http.MethodPut, "/api/users/u-test/password"},
		{http.MethodDelete, "/api/users/u-test"},
		{http.MethodGet, "/api/groups"},
		{http.MethodPost, "/api/groups"},
		{http.MethodPatch, "/api/groups/g-1"},
		{http.MethodDelete, "/api/groups/g-1"},
		{http.MethodGet, "/api/groups/g-1/members"},
		{http.MethodPut, "/api/groups/g-1/members"},
		{http.MethodGet, "/api/groups/g-1/grants"},
		{http.MethodPut, "/api/groups/g-1/grants"},
	}
	for _, ep := range endpoints {
		rec, body := env.do(t, ep.method, ep.path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 未登录应返回 401，实际 %d", ep.method, ep.path, rec.Code)
			continue
		}
		if body.Error.Code != "unauthorized" {
			t.Errorf("%s %s 错误码应为 unauthorized，实际 %q", ep.method, ep.path, body.Error.Code)
		}
	}
}

// containsSub 是 strings.Contains 的测试内薄封装，避免为一个函数引入 strings。
func containsSub(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// 引用 store/user 包以保证测试文件与其模型同源（并在编译期发现签名变化）。
var _ = store.ErrConflict
var _ = user.RoleAdmin
