package store

// agents_admin_test.go 覆盖服务器记录的管理操作。
//
// 最重要的一条是 DeleteAgent 不连带删除 compose_stacks——
// 编排是用户手写的资产，绝不能因为服务器记录没了就消失。

import (
	"context"
	"errors"
	"testing"
	"time"
)

// timeNow 返回当前时间的 Unix 秒。
func timeNow() int64 { return time.Now().UTC().Unix() }

// timeNowTime 返回当前时间，供需要 time.Time 的字段使用。
func timeNowTime() time.Time { return time.Now().UTC() }

// seedAgent 写入一条 Agent 记录。
func seedAgent(t *testing.T, db *DB, id, name string) {
	t.Helper()
	now := timeNow()
	err := db.UpsertAgent(context.Background(), AgentRecord{
		ID:        id,
		Name:      name,
		Status:    "online",
		Runtime:   "docker",
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("准备 Agent 失败: %v", err)
	}
}

// seedStack 写入一条编排记录。
//
// ID 必须显式给：compose_stacks 的主键是 TEXT NOT NULL（不是自增），
// 留空会让多条记录都撞在空串主键上，报"记录已存在"。
func seedStack(t *testing.T, db *DB, agentID, name string) {
	t.Helper()
	now := timeNowTime()
	err := db.CreateComposeStack(context.Background(), ComposeStack{
		ID:      agentID + "/" + name,
		AgentID: agentID, Name: name, Content: "services: {}\n",
		ConfigHash: "abc", Version: 1, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("准备编排失败: %v", err)
	}
}

// TestAgentAdminFieldsDefault 验证新字段的默认值。
//
// 迁移加了 NOT NULL DEFAULT，但"默认值对不对"要实测：
// 如果 display_name 默认成 NULL，扫描会失败导致整个列表接口 500。
func TestAgentAdminFieldsDefault(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")

	rec, err := db.GetAgent(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.DisplayName != "" {
		t.Errorf("显示名默认应为空串，实际 %q", rec.DisplayName)
	}
	if rec.Note != "" {
		t.Errorf("备注默认应为空串，实际 %q", rec.Note)
	}
	if rec.Disabled {
		t.Error("禁用状态默认应为 false")
	}
}

// TestUpdateAgentAdminDisplayName 验证改显示名。
func TestUpdateAgentAdminDisplayName(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")
	ctx := context.Background()

	name := "生产-东京"
	rec, err := db.UpdateAgentAdmin(ctx, "a1", AgentAdminFields{DisplayName: &name}, timeNow())
	if err != nil {
		t.Fatalf("更新失败: %v", err)
	}
	if rec.DisplayName != name {
		t.Errorf("显示名应为 %q，实际 %q", name, rec.DisplayName)
	}
	// 关键：Name（主机名）不能被改名操作带跑。
	if rec.Name != "host-1" {
		t.Errorf("主机名不应被改动，实际 %q", rec.Name)
	}
}

// TestUpdateAgentAdminPartial 验证 PATCH 语义：只改传入的字段。
//
// 这是 PATCH 与 PUT 的区别所在。如果实现里把没传的字段写成零值，
// 用户改个备注就会把显示名和标签一起清空——这是很常见的事故。
func TestUpdateAgentAdminPartial(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")
	ctx := context.Background()

	name := "生产-东京"
	if _, err := db.UpdateAgentAdmin(ctx, "a1",
		AgentAdminFields{DisplayName: &name}, timeNow()); err != nil {
		t.Fatal(err)
	}

	// 只改备注，不动显示名。
	note := "负责商品服务"
	rec, err := db.UpdateAgentAdmin(ctx, "a1",
		AgentAdminFields{Note: &note}, timeNow())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Note != note {
		t.Errorf("备注应为 %q，实际 %q", note, rec.Note)
	}
	if rec.DisplayName != name {
		t.Errorf("只改备注时显示名应保持不变，实际 %q", rec.DisplayName)
	}
}

// TestUpdateAgentAdminClearNote 验证能把备注清空。
//
// 零值表示"不改"的话就永远清不掉备注，所以字段必须用指针。
func TestUpdateAgentAdminClearNote(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")
	ctx := context.Background()

	note := "旧备注"
	if _, err := db.UpdateAgentAdmin(ctx, "a1",
		AgentAdminFields{Note: &note}, timeNow()); err != nil {
		t.Fatal(err)
	}

	empty := ""
	rec, err := db.UpdateAgentAdmin(ctx, "a1", AgentAdminFields{Note: &empty}, timeNow())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Note != "" {
		t.Errorf("备注应被清空，实际 %q", rec.Note)
	}
}

// TestUpdateAgentAdminLabels 验证标签更新。
func TestUpdateAgentAdminLabels(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")

	labels := map[string]string{"env": "prod", "region": "tokyo"}
	rec, err := db.UpdateAgentAdmin(context.Background(), "a1",
		AgentAdminFields{Labels: &labels}, timeNow())
	if err != nil {
		t.Fatal(err)
	}
	if rec.Labels["env"] != "prod" || rec.Labels["region"] != "tokyo" {
		t.Errorf("标签更新失败: %+v", rec.Labels)
	}
}

// TestUpdateAgentAdminEmptyIsNoop 验证不传任何字段时什么都不改。
func TestUpdateAgentAdminEmptyIsNoop(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")

	before, err := db.GetAgent(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	// 传一个很大的 now：如果实现真的写了库，updated_at 会变。
	after, err := db.UpdateAgentAdmin(context.Background(), "a1", AgentAdminFields{}, before.UpdatedAt+1000)
	if err != nil {
		t.Fatal(err)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("无字段可改时不应更新 updated_at，%d → %d", before.UpdatedAt, after.UpdatedAt)
	}
}

// TestUpdateAgentAdminNotFound 验证不存在的 Agent 返回 ErrNotFound。
func TestUpdateAgentAdminNotFound(t *testing.T) {
	db := newTestDB(t)
	name := "x"
	_, err := db.UpdateAgentAdmin(context.Background(), "ghost",
		AgentAdminFields{DisplayName: &name}, timeNow())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("应返回 ErrNotFound，实际 %v", err)
	}
}

// TestSetAgentDisabled 验证禁用/启用切换。
func TestSetAgentDisabled(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")
	ctx := context.Background()

	if err := db.SetAgentDisabled(ctx, "a1", true, timeNow()); err != nil {
		t.Fatal(err)
	}
	got, err := db.IsAgentDisabled(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("应已禁用")
	}

	if err := db.SetAgentDisabled(ctx, "a1", false, timeNow()); err != nil {
		t.Fatal(err)
	}
	got, err = db.IsAgentDisabled(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("应已启用")
	}
}

// TestIsAgentDisabledNotFound 验证查询不存在的 Agent。
//
// 返回 ErrNotFound 而不是 (false, nil)：调用方需要区分
// "这台机器没被禁用"和"这台机器还不存在"（后者是首次注册，正常放行）。
func TestIsAgentDisabledNotFound(t *testing.T) {
	db := newTestDB(t)
	disabled, err := db.IsAgentDisabled(context.Background(), "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("应返回 ErrNotFound，实际 %v", err)
	}
	if disabled {
		t.Error("不存在的 Agent 不应被判为禁用")
	}
}

// TestSetAgentDisabledNotFound 验证对不存在的 Agent 设置禁用被拒。
func TestSetAgentDisabledNotFound(t *testing.T) {
	db := newTestDB(t)
	if err := db.SetAgentDisabled(context.Background(), "ghost", true, timeNow()); !errors.Is(err, ErrNotFound) {
		t.Errorf("应返回 ErrNotFound，实际 %v", err)
	}
}

// TestDeleteAgent 验证删除。
func TestDeleteAgent(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")
	ctx := context.Background()

	if err := db.DeleteAgent(ctx, "a1"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if _, err := db.GetAgent(ctx, "a1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后应查不到，实际 %v", err)
	}
}

// TestDeleteAgentNotFound 验证重复删除返回 ErrNotFound（而不是静默成功）。
func TestDeleteAgentNotFound(t *testing.T) {
	db := newTestDB(t)
	if err := db.DeleteAgent(context.Background(), "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("应返回 ErrNotFound，实际 %v", err)
	}
}

// TestDeleteAgentKeepsComposeStacks 是本任务最重要的一条测试。
//
// **删除服务器记录绝不能连带删除编排**。
//
// 编排内容是用户手写的资产（可能调试了几小时），而 Agent 记录只是
// "这台机器归谁管"的登记。删掉登记不该连坐用户的文件。
// 更实际的风险：Agent 可能只是换机重装或暂时下线，
// 若连带删了编排，用户会永久失去内容且难以察觉。
func TestDeleteAgentKeepsComposeStacks(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")
	ctx := context.Background()

	// 建两条编排挂在 a1 名下。
	for _, name := range []string{"web", "db"} {
		seedStack(t, db, "a1", name)
	}

	if err := db.DeleteAgent(ctx, "a1"); err != nil {
		t.Fatalf("删除服务器失败: %v", err)
	}

	// 编排必须还在。
	for _, name := range []string{"web", "db"} {
		stack, err := db.GetComposeStack(ctx, "a1", name)
		if err != nil {
			t.Fatalf("编排 %s 不应被删除: %v", name, err)
		}
		if stack.Content != "services: {}\n" {
			t.Errorf("编排 %s 的内容不应被改动", name)
		}
	}

	// 它们现在应该是孤儿。
	n, err := db.CountOrphanComposeStacks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("应有 2 条孤儿编排，实际 %d", n)
	}
}

// TestCountOrphanComposeStacks 验证孤儿统计。
func TestCountOrphanComposeStacks(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")
	seedAgent(t, db, "a2", "host-2")
	ctx := context.Background()

	seedStack(t, db, "a1", "web")
	seedStack(t, db, "a2", "db")

	// 刚开始没有孤儿。
	if n, err := db.CountOrphanComposeStacks(ctx); err != nil || n != 0 {
		t.Fatalf("初始孤儿数应为 0，实际 %d (err=%v)", n, err)
	}

	// 删掉 a1 后它的编排变成孤儿。
	if err := db.DeleteAgent(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if n, err := db.CountOrphanComposeStacks(ctx); err != nil || n != 1 {
		t.Errorf("应有 1 条孤儿，实际 %d (err=%v)", n, err)
	}
}

// TestCountComposeStacksByAgent 验证按 Agent 统计编排数量。
//
// 删除服务器前要用它算出"这次操作会让多少条编排失去归属"。
func TestCountComposeStacksByAgent(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")

	for _, name := range []string{"web", "db", "cache"} {
		seedStack(t, db, "a1", name)
	}

	n, err := db.CountComposeStacks(context.Background(), "a1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("应有 3 条编排，实际 %d", n)
	}
}

// TestAgentListIncludesAdminFields 验证列表接口能取到新字段。
//
// 扫描顺序错位是这类"加列"改动最常见的 bug：编译期发现不了，
// 运行时会把 note 读进 display_name 之类，界面显示乱码。
func TestAgentListIncludesAdminFields(t *testing.T) {
	db := newTestDB(t)
	seedAgent(t, db, "a1", "host-1")
	ctx := context.Background()

	name, note := "东京", "生产环境"
	if _, err := db.UpdateAgentAdmin(ctx, "a1",
		AgentAdminFields{DisplayName: &name, Note: &note}, timeNow()); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAgentDisabled(ctx, "a1", true, timeNow()); err != nil {
		t.Fatal(err)
	}

	list, err := db.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 条记录，实际 %d", len(list))
	}
	got := list[0]
	if got.DisplayName != name {
		t.Errorf("显示名应为 %q，实际 %q", name, got.DisplayName)
	}
	if got.Note != note {
		t.Errorf("备注应为 %q，实际 %q", note, got.Note)
	}
	if !got.Disabled {
		t.Error("应标记为已禁用")
	}
	if got.Name != "host-1" {
		t.Errorf("主机名应为 host-1，实际 %q", got.Name)
	}
}
