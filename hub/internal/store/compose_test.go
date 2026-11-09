package store

// compose_test.go 覆盖编排表（compose_stacks）的仓储行为。
//
// 重点是被 HTTP 层掩盖的两件事：
//   - 乐观锁在 SQL 层是否真的生效（RowsAffected 的语义容易写错）；
//   - 记录不存在与版本冲突是否被正确区分成两种错误。

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mkStack 构造一条编排记录。
func mkStack(agentID, name, content string) ComposeStack {
	now := time.Now().UTC()
	return ComposeStack{
		ID: "stk-" + agentID + "-" + name, AgentID: agentID, Name: name,
		Content: content, ConfigHash: "hash-" + content, Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}
}

// TestCreateComposeStackConflict 验证同一 Agent 下项目名重复返回 ErrConflict。
func TestCreateComposeStackConflict(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateComposeStack(ctx, mkStack("a1", "web", "v1")); err != nil {
		t.Fatalf("首次创建应成功: %v", err)
	}
	err := db.CreateComposeStack(ctx, mkStack("a1", "web", "v2"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("同 Agent 下重名应返回 ErrConflict，实际 %v", err)
	}

	// 不同 Agent 上同名是允许的：项目名只在 Agent 内部唯一。
	if err := db.CreateComposeStack(ctx, mkStack("a2", "web", "v1")); err != nil {
		t.Errorf("不同 Agent 上同名应允许，实际 %v", err)
	}
}

// TestGetComposeStackNotFound 验证查询不存在返回 ErrNotFound。
func TestGetComposeStackNotFound(t *testing.T) {
	db := newTestDB(t)
	_, err := db.GetComposeStack(context.Background(), "a1", "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("应返回 ErrNotFound，实际 %v", err)
	}
}

// TestListComposeStacksOrder 验证按项目名升序且只返回该 Agent 的记录。
func TestListComposeStacksOrder(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"zeta", "alpha", "mid"} {
		if err := db.CreateComposeStack(ctx, mkStack("a1", name, "x")); err != nil {
			t.Fatal(err)
		}
	}
	// 另一台 Agent 的记录不应混进来。
	if err := db.CreateComposeStack(ctx, mkStack("a2", "other", "x")); err != nil {
		t.Fatal(err)
	}

	list, err := db.ListComposeStacks(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("应返回 3 条，实际 %d", len(list))
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, n := range want {
		if list[i].Name != n {
			t.Errorf("第 %d 条应为 %s，实际 %s", i, n, list[i].Name)
		}
	}
}

// TestUpdateComposeStackVersion 验证乐观锁：正确版本才更新，版本号自增。
func TestUpdateComposeStackVersion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateComposeStack(ctx, mkStack("a1", "web", "v1")); err != nil {
		t.Fatal(err)
	}

	// 版本正确：更新成功，版本变 2。
	updated, ok, err := db.UpdateComposeStack(ctx, "a1", "web", 1, "v2", "hash-v2")
	if err != nil {
		t.Fatalf("正确版本更新不应报错: %v", err)
	}
	if !ok {
		t.Fatal("正确版本应命中")
	}
	if updated.Version != 2 {
		t.Errorf("版本应自增到 2，实际 %d", updated.Version)
	}
	if updated.Content != "v2" || updated.ConfigHash != "hash-v2" {
		t.Errorf("内容与哈希应更新，实际 %q / %q", updated.Content, updated.ConfigHash)
	}

	// 版本过期：不命中（ok=false），且不报错。
	stale, ok, err := db.UpdateComposeStack(ctx, "a1", "web", 1, "v3", "hash-v3")
	if err != nil {
		t.Fatalf("版本冲突不应返回错误（由上层映射 409）: %v", err)
	}
	if ok {
		t.Fatal("过期版本不应命中")
	}
	if stale.Name != "" {
		t.Errorf("未命中时应返回零值，实际 %+v", stale)
	}

	// 内容必须保持第二次写入的值。
	cur, err := db.GetComposeStack(ctx, "a1", "web")
	if err != nil {
		t.Fatal(err)
	}
	if cur.Content != "v2" {
		t.Fatalf("版本冲突后内容不能被覆盖，实际 %q", cur.Content)
	}
}

// TestUpdateComposeStackNotFound 验证更新不存在的记录返回 ErrNotFound（不是冲突）。
//
// 这个区分至关重要：上层据此返回 404 而不是 409。
func TestUpdateComposeStackNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, ok, err := db.UpdateComposeStack(ctx, "a1", "ghost", 1, "v2", "h")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("更新不存在的记录应返回 ErrNotFound，实际 err=%v ok=%v", err, ok)
	}
}

// TestDeleteComposeStack 验证删除与"删除不存在"的错误区分。
func TestDeleteComposeStack(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateComposeStack(ctx, mkStack("a1", "web", "v1")); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteComposeStack(ctx, "a1", "web"); err != nil {
		t.Fatalf("删除应成功: %v", err)
	}
	if _, err := db.GetComposeStack(ctx, "a1", "web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除后应查不到，实际 %v", err)
	}
	// 再删一次：ErrNotFound，幂等语义交给上层决定如何响应。
	if err := db.DeleteComposeStack(ctx, "a1", "web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("删除不存在的记录应返回 ErrNotFound，实际 %v", err)
	}
}

// TestCountComposeStacks 验证按 Agent 统计数量。
func TestCountComposeStacks(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, n := range []string{"a", "b", "c"} {
		if err := db.CreateComposeStack(ctx, mkStack("a1", n, "x")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CreateComposeStack(ctx, mkStack("a2", "a", "x")); err != nil {
		t.Fatal(err)
	}

	n, err := db.CountComposeStacks(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("a1 应有 3 条，实际 %d", n)
	}
	n, _ = db.CountComposeStacks(ctx, "a2")
	if n != 1 {
		t.Errorf("a2 应有 1 条，实际 %d", n)
	}
}
