package store

// store_test.go 覆盖仓储层的约束行为。
//
// 为什么需要独立于 HTTP 层的测试：这里验证的是「数据库保证」而非「接口行为」，
// 例如重名冲突必须归一化成 ErrConflict、事务内的整体替换不能留半截状态。
// 这些性质一旦破坏，上层所有接口都会跟着错，但表现会分散在很多用例里。

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"arkade/hub/internal/user"
)

// newTestDB 打开一个临时库。
func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// mkUser 构造一个测试用户。
func mkUser(id, username string, role user.Role) user.User {
	now := time.Now().UTC()
	return user.User{
		ID: id, Username: username, DisplayName: username,
		PasswordHash: "x", Role: role, CreatedAt: now, UpdatedAt: now,
	}
}

// TestCreateUserConflict 验证重名被归一化成 ErrConflict（上层才能映射 409）。
func TestCreateUserConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateUser(ctx, mkUser("u1", "alice", user.RoleUser)); err != nil {
		t.Fatalf("首次创建应成功: %v", err)
	}
	err := db.CreateUser(ctx, mkUser("u2", "alice", user.RoleUser))
	if err == nil {
		t.Fatal("重名应当报错")
	}
	// 关键断言：错误必须能被 errors.Is 识别，而不是只能靠文本匹配。
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("重名错误应满足 errors.Is(err, ErrConflict)，实际 %v", err)
	}
}

// TestCreateGroupConflict 验证组名重名归一化成 ErrConflict。
func TestCreateGroupConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	g := user.Group{ID: "g1", Name: "运维", CreatedAt: time.Now().UTC()}
	if err := db.CreateGroup(ctx, g); err != nil {
		t.Fatalf("首次创建应成功: %v", err)
	}
	err := db.CreateGroup(ctx, user.Group{ID: "g2", Name: "运维", CreatedAt: time.Now().UTC()})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("组名重名应满足 errors.Is(err, ErrConflict)，实际 %v", err)
	}
}

// TestForeignKeysEnabled 验证 DSN 里的 PRAGMA foreign_keys(1) 确实生效。
//
// 这条测试的价值：整个删除逻辑都依赖外键是否开启。若某天 DSN 被改动导致
// 外键失效，级联删除会静默不生效并留下孤儿行——那种问题极难排查。
func TestForeignKeysEnabled(t *testing.T) {
	db := newTestDB(t)
	var on int
	if err := db.db.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatalf("读取 PRAGMA 失败: %v", err)
	}
	if on != 1 {
		t.Fatalf("外键约束必须开启（PRAGMA foreign_keys = %d）", on)
	}
}

// TestDeleteUserCascades 验证删除用户会清理组成员关系与会话。
func TestDeleteUserCascades(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateUser(ctx, mkUser("u1", "alice", user.RoleUser)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateGroup(ctx, user.Group{ID: "g1", Name: "组", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetGroupMembers(ctx, "g1", []string{"u1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSession(ctx, "hash1", "u1", time.Now().Unix(), time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteUser(ctx, "u1"); err != nil {
		t.Fatalf("删除用户失败: %v", err)
	}

	// 组本身还在。
	if _, err := db.GetGroup(ctx, "g1"); err != nil {
		t.Errorf("删除用户不应影响用户组: %v", err)
	}
	// 成员关系与会话已清。
	members, err := db.ListGroupMembers(ctx, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Errorf("组成员关系应被清理，实际 %+v", members)
	}
	if _, err := db.SessionUserID(ctx, "hash1", time.Now().Unix()); !errors.Is(err, ErrNotFound) {
		t.Errorf("用户会话应被清理，实际 err=%v", err)
	}
}

// TestDeleteGroupCascades 验证删除组会清理成员关系与授权，但不删用户。
func TestDeleteGroupCascades(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateUser(ctx, mkUser("u1", "alice", user.RoleUser)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateGroup(ctx, user.Group{ID: "g1", Name: "组", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetGroupMembers(ctx, "g1", []string{"u1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.SetGroupGrants(ctx, "g1", []user.GroupGrant{{GroupID: "g1", AgentID: "a1", Permission: user.PermView}}); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteGroup(ctx, "g1"); err != nil {
		t.Fatalf("删除组失败: %v", err)
	}
	if _, err := db.GetUserByID(ctx, "u1"); err != nil {
		t.Errorf("删除组不应删除成员用户: %v", err)
	}
	grants, err := db.ListUserGrants(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Errorf("删组后授权应失效，实际 %+v", grants)
	}
}

// TestSetGroupMembersReplaces 验证整体设置是「替换」而不是「追加」，且能清空。
func TestSetGroupMembersReplaces(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, u := range []struct{ id, name string }{{"u1", "alice"}, {"u2", "bob"}} {
		if err := db.CreateUser(ctx, mkUser(u.id, u.name, user.RoleUser)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateGroup(ctx, user.Group{ID: "g1", Name: "组", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	if err := db.SetGroupMembers(ctx, "g1", []string{"u1"}); err != nil {
		t.Fatal(err)
	}
	// 换成 u2：u1 应当被移除。
	if err := db.SetGroupMembers(ctx, "g1", []string{"u2", "u2"}); err != nil {
		t.Fatal(err)
	}
	members, err := db.ListGroupMembers(ctx, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].ID != "u2" {
		t.Fatalf("应只剩 u2（且去重），实际 %+v", members)
	}

	// 清空。
	if err := db.SetGroupMembers(ctx, "g1", nil); err != nil {
		t.Fatal(err)
	}
	members, err = db.ListGroupMembers(ctx, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Fatalf("清空后不应有成员，实际 %+v", members)
	}
}

// TestSetGroupMembersMissingGroup 验证给不存在的组设成员返回 ErrNotFound。
func TestSetGroupMembersMissingGroup(t *testing.T) {
	db := newTestDB(t)
	err := db.SetGroupMembers(context.Background(), "g-ghost", nil)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的组应返回 ErrNotFound，实际 %v", err)
	}
}

// TestCountActiveAdmins 验证「有效管理员」口径：admin 且未禁用，且能排除指定用户。
func TestCountActiveAdmins(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_ = db.CreateUser(ctx, mkUser("a1", "admin1", user.RoleAdmin))
	_ = db.CreateUser(ctx, mkUser("a2", "admin2", user.RoleAdmin))
	_ = db.CreateUser(ctx, mkUser("n1", "normal", user.RoleUser))

	n, err := db.CountActiveAdmins(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("有效管理员应为 2，实际 %d", n)
	}
	// 排除 a1 后只剩 a2。
	n, err = db.CountActiveAdmins(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("排除 a1 后应为 1，实际 %d", n)
	}

	// 禁用 a2 后，排除 a1 就变成 0（这正是「最后一个管理员」的判定场景）。
	disabled := true
	if err := db.UpdateUser(ctx, "a2", UserUpdate{Disabled: &disabled}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	n, _ = db.CountActiveAdmins(ctx, "a1")
	if n != 0 {
		t.Fatalf("禁用 a2 后排除 a1 应为 0，实际 %d", n)
	}
}

// TestSetGroupGrantsReplaces 验证授权整体替换与档位字符串落库。
func TestSetGroupGrantsReplaces(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateGroup(ctx, user.Group{ID: "g1", Name: "组", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	err := db.SetGroupGrants(ctx, "g1", []user.GroupGrant{
		{GroupID: "g1", AgentID: "a1", Permission: user.PermView},
		{GroupID: "g1", AgentID: "a2", Permission: user.PermManage},
	})
	if err != nil {
		t.Fatal(err)
	}
	grants, err := db.ListGroupGrants(ctx, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("应有 2 条授权，实际 %+v", grants)
	}
	// 按 agent_id 升序。
	if grants[0].AgentID != "a1" || grants[0].Permission != user.PermView {
		t.Errorf("第 1 条不符: %+v", grants[0])
	}
	if grants[1].Permission != user.PermManage {
		t.Errorf("第 2 条档位应为 manage，实际 %v", grants[1].Permission)
	}

	// 替换成单条。
	if err := db.SetGroupGrants(ctx, "g1", []user.GroupGrant{
		{GroupID: "g1", AgentID: "a3", Permission: user.PermOperate},
	}); err != nil {
		t.Fatal(err)
	}
	grants, _ = db.ListGroupGrants(ctx, "g1")
	if len(grants) != 1 || grants[0].AgentID != "a3" {
		t.Fatalf("替换后应只剩 a3，实际 %+v", grants)
	}
}

// TestListGroupsCounts 验证组的成员数与授权数聚合正确（不会被 JOIN 放大）。
func TestListGroupsCounts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateGroup(ctx, user.Group{ID: "g1", Name: "组", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ id, name string }{{"u1", "alice"}, {"u2", "bob"}} {
		if err := db.CreateUser(ctx, mkUser(u.id, u.name, user.RoleUser)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetGroupMembers(ctx, "g1", []string{"u1", "u2"}); err != nil {
		t.Fatal(err)
	}
	// 两条授权 + 两个成员 = 4 行 JOIN 结果，若无 DISTINCT 就会算成 2/2 或 4。
	if err := db.SetGroupGrants(ctx, "g1", []user.GroupGrant{
		{GroupID: "g1", AgentID: "a1", Permission: user.PermView},
		{GroupID: "g1", AgentID: "a2", Permission: user.PermView},
	}); err != nil {
		t.Fatal(err)
	}

	groups, err := db.ListGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("应有 1 个组，实际 %d", len(groups))
	}
	if groups[0].MemberCount != 2 || groups[0].GrantCount != 2 {
		t.Fatalf("成员数应为 2、授权数应为 2，实际 %d/%d", groups[0].MemberCount, groups[0].GrantCount)
	}
}

// TestUpdateUserPartial 验证局部更新只改指定字段。
func TestUpdateUserPartial(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateUser(ctx, mkUser("u1", "alice", user.RoleUser)); err != nil {
		t.Fatal(err)
	}

	display := "爱丽丝"
	if err := db.UpdateUser(ctx, "u1", UserUpdate{DisplayName: &display}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	u, err := db.GetUserByID(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if u.DisplayName != "爱丽丝" {
		t.Errorf("显示名应已更新，实际 %q", u.DisplayName)
	}
	// 未指定的字段保持不变。
	if u.Role != user.RoleUser {
		t.Errorf("未指定的角色不应改变，实际 %v", u.Role)
	}

	// 更新不存在的用户返回 ErrNotFound。
	if err := db.UpdateUser(ctx, "u-ghost", UserUpdate{DisplayName: &display}, time.Now().Unix()); !errors.Is(err, ErrNotFound) {
		t.Errorf("更新不存在的用户应返回 ErrNotFound，实际 %v", err)
	}
}

// TestUpdateUserPasswordClearsSessions 验证改密会清除该用户全部会话。
func TestUpdateUserPasswordClearsSessions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateUser(ctx, mkUser("u1", "alice", user.RoleUser)); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	if err := db.CreateSession(ctx, "h1", "u1", now, now+3600); err != nil {
		t.Fatal(err)
	}

	if err := db.UpdateUserPassword(ctx, "u1", "newhash", now); err != nil {
		t.Fatal(err)
	}
	u, err := db.GetUserByID(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if u.PasswordHash != "newhash" {
		t.Errorf("密码哈希应已更新，实际 %q", u.PasswordHash)
	}
	if _, err := db.SessionUserID(ctx, "h1", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("改密后会话应被清除，实际 err=%v", err)
	}
}

// TestUsersMissing 验证存在性检查返回具体缺失的 ID。
func TestUsersMissing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateUser(ctx, mkUser("u1", "alice", user.RoleUser)); err != nil {
		t.Fatal(err)
	}

	missing, err := db.UsersMissing(ctx, []string{"u1", "u-ghost", "u-ghost"})
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != "u-ghost" {
		t.Fatalf("应只返回去重后的缺失 ID u-ghost，实际 %v", missing)
	}
}

// TestListUserGrantsVersusGroupGrants 是那次真实 bug 的回归测试。
//
// 曾经出现过：ListGroupGrants 被误用成「按用户查」，因为两个方法的签名
// 完全相同（都是 (string) ([]user.GroupGrant, error)），编译通过、
// 权限判定却永远查不到授权。这里把两个方法的语义差异固定住。
func TestListUserGrantsVersusGroupGrants(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateUser(ctx, mkUser("u1", "alice", user.RoleUser)); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateGroup(ctx, user.Group{ID: "g1", Name: "组1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateGroup(ctx, user.Group{ID: "g2", Name: "组2", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	// u1 属于 g1，g1 授予 a1；g2 授予 a2 但 u1 不在 g2 里。
	_ = db.SetGroupMembers(ctx, "g1", []string{"u1"})
	_ = db.SetGroupGrants(ctx, "g1", []user.GroupGrant{{GroupID: "g1", AgentID: "a1", Permission: user.PermView}})
	_ = db.SetGroupGrants(ctx, "g2", []user.GroupGrant{{GroupID: "g2", AgentID: "a2", Permission: user.PermView}})

	// 按用户查：只应拿到 g1/a1。
	userGrants, err := db.ListUserGrants(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(userGrants) != 1 || userGrants[0].AgentID != "a1" {
		t.Fatalf("u1 应只拿到 a1，实际 %+v", userGrants)
	}

	// 按组查：g1 只有 a1。
	groupGrants, err := db.ListGroupGrants(ctx, "g1")
	if err != nil {
		t.Fatal(err)
	}
	if len(groupGrants) != 1 || groupGrants[0].AgentID != "a1" {
		t.Fatalf("g1 应只有 a1，实际 %+v", groupGrants)
	}

	// 把 userID 当作 groupID 传给 ListGroupGrants 会得到空——这正是当初
	// 权限静默失效的表现，固定住这个事实，避免有人再混用。
	wrong, err := db.ListGroupGrants(ctx, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(wrong) != 0 {
		t.Fatalf("按用户 ID 查组授权应为空（说明两者不能混用），实际 %+v", wrong)
	}
}
