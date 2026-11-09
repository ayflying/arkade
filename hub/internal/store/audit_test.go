package store

// audit_test.go 覆盖审计日志的写入、筛选、分页与清理。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"arkade/hub/internal/audit"
)

// mkAudit 构造一条审计记录。
func mkAudit(userID, action string, at time.Time) audit.Record {
	return audit.Record{
		UserID:    userID,
		Username:  "user-" + userID,
		Action:    action,
		Result:    audit.ResultSuccess,
		CreatedAt: at,
	}
}

// TestWriteAudit 验证基本写入与回读。
func TestWriteAudit(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()

	rec := audit.Record{
		UserID:     "u1",
		Username:   "admin",
		AgentID:    "a1",
		AgentName:  "东京",
		Action:     audit.ActionContainerRemove,
		Target:     "nginx",
		Detail:     "强制删除",
		Result:     audit.ResultSuccess,
		DurationMs: 42,
		CreatedAt:  now,
	}
	if err := db.WriteAudit(rec); err != nil {
		t.Fatalf("写入失败: %v", err)
	}

	list, total, err := db.ListAuditLogs(context.Background(), AuditFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("应有 1 条记录，实际 total=%d len=%d", total, len(list))
	}
	got := list[0]
	if got.Username != "admin" || got.AgentName != "东京" {
		t.Errorf("冗余名称字段丢失: %+v", got)
	}
	if got.Action != audit.ActionContainerRemove || got.Target != "nginx" {
		t.Errorf("动作或目标丢失: %+v", got)
	}
	if got.DurationMs != 42 {
		t.Errorf("耗时应为 42，实际 %d", got.DurationMs)
	}
	if got.ID == "" {
		t.Error("ID 应被自动补上")
	}
}

// TestWriteAuditGeneratesID 验证漏填 ID 时自动补。
//
// 审计的价值在于"记录下来了"：字段不全但记下了，
// 远好过因为漏一个 ID 整条丢失。
func TestWriteAuditGeneratesID(t *testing.T) {
	db := newTestDB(t)
	for i := 0; i < 3; i++ {
		if err := db.WriteAudit(mkAudit("u1", audit.ActionUserCreate, time.Now().UTC())); err != nil {
			t.Fatal(err)
		}
	}
	list, total, err := db.ListAuditLogs(context.Background(), AuditFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("三条记录都应有各自的 ID，实际 total=%d", total)
	}
	seen := map[string]bool{}
	for _, r := range list {
		if seen[r.ID] {
			t.Errorf("ID 重复: %q", r.ID)
		}
		seen[r.ID] = true
	}
}

// TestListAuditOrdering 验证按时间倒序。
//
// 审计列表的默认用法是"最近发生了什么"，正序会让用户翻到最后一页
// 才能看到刚才的操作。
func TestListAuditOrdering(t *testing.T) {
	db := newTestDB(t)
	base := time.Now().UTC().Add(-time.Hour)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := db.WriteAudit(mkAudit("u1", audit.ActionUserCreate,
			base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}

	list, _, err := db.ListAuditLogs(ctx, AuditFilters{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(list); i++ {
		if list[i].CreatedAt.After(list[i-1].CreatedAt) {
			t.Fatalf("应按时间倒序，第 %d 条比前一条更新", i)
		}
	}
}

// TestListAuditFilterByUser 验证按用户筛选。
func TestListAuditFilterByUser(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	if err := db.WriteAudit(mkAudit("u1", audit.ActionUserCreate, now)); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteAudit(mkAudit("u2", audit.ActionUserCreate, now)); err != nil {
		t.Fatal(err)
	}

	list, total, err := db.ListAuditLogs(context.Background(), AuditFilters{UserID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("应只有 1 条，实际 total=%d len=%d", total, len(list))
	}
	if list[0].UserID != "u1" {
		t.Errorf("筛选结果用户不符: %q", list[0].UserID)
	}
}

// TestListAuditFilterByActionAndResult 验证按动作与结果筛选。
func TestListAuditFilterByActionAndResult(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	ctx := context.Background()

	ok := mkAudit("u1", audit.ActionContainerRemove, now)
	failed := mkAudit("u1", audit.ActionContainerRemove, now)
	failed.Result = audit.ResultFailed
	failed.Error = "容器正在运行"

	if err := db.WriteAudit(ok); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteAudit(failed); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteAudit(mkAudit("u1", audit.ActionUserCreate, now)); err != nil {
		t.Fatal(err)
	}

	list, total, err := db.ListAuditLogs(ctx, AuditFilters{
		Action: audit.ActionContainerRemove, Result: audit.ResultFailed,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(list) != 1 {
		t.Fatalf("应只有 1 条失败记录，实际 total=%d", total)
	}
	if list[0].Error != "容器正在运行" {
		t.Errorf("错误原因应被保留，实际 %q", list[0].Error)
	}
}

// TestListAuditFilterByTimeRange 验证时间范围筛选。
func TestListAuditFilterByTimeRange(t *testing.T) {
	db := newTestDB(t)
	base := time.Now().UTC().Add(-10 * 24 * time.Hour)
	ctx := context.Background()

	// 第 0、5、9 天各一条。
	for _, d := range []int{0, 5, 9} {
		if err := db.WriteAudit(mkAudit("u1", audit.ActionUserCreate,
			base.Add(time.Duration(d)*24*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}

	from := base.Add(3 * 24 * time.Hour).Unix()
	to := base.Add(7 * 24 * time.Hour).Unix()
	_, total, err := db.ListAuditLogs(ctx, AuditFilters{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("区间内应只有 1 条，实际 %d", total)
	}
}

// TestListAuditPagination 验证分页。
//
// 同时验证 total 与当前页无关：前端靠 total 画分页器，
// 若 total 返回的是"本页条数"，分页器永远只有一页。
func TestListAuditPagination(t *testing.T) {
	db := newTestDB(t)
	base := time.Now().UTC().Add(-time.Hour)
	ctx := context.Background()

	for i := 0; i < 12; i++ {
		if err := db.WriteAudit(mkAudit("u1", audit.ActionUserCreate,
			base.Add(time.Duration(i)*time.Minute))); err != nil {
			t.Fatal(err)
		}
	}

	page1, total, err := db.ListAuditLogs(ctx, AuditFilters{Limit: 5, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if total != 12 {
		t.Errorf("total 应为 12（与分页无关），实际 %d", total)
	}
	if len(page1) != 5 {
		t.Errorf("第一页应有 5 条，实际 %d", len(page1))
	}

	page3, _, err := db.ListAuditLogs(ctx, AuditFilters{Limit: 5, Offset: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3) != 2 {
		t.Errorf("第三页应有 2 条，实际 %d", len(page3))
	}
}

// TestListAuditLimitCapped 验证 limit 上限。
//
// 不设上限时一个 limit=999999 就能让 Hub 内存暴涨。
func TestListAuditLimitCapped(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if err := db.WriteAudit(mkAudit("u1", audit.ActionUserCreate, now)); err != nil {
			t.Fatal(err)
		}
	}
	// 传一个超大的 limit，结果条数不应超过 AuditMaxLimit。
	list, _, err := db.ListAuditLogs(context.Background(), AuditFilters{Limit: 999999})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) > AuditMaxLimit {
		t.Errorf("结果不应超过上限 %d，实际 %d", AuditMaxLimit, len(list))
	}
}

// TestPruneAuditLogs 验证过期清理。
//
// 90 天前的记录必须被删掉，否则表会无限膨胀；
// 而 89 天前的必须留着，否则用户查三个月前的事就查不到了。
//
// 注意：WriteAudit 内部会顺带触发一次节流清理（见 maybePruneAudit），
// 而首次写入时 lastPrune 是零值，间隔判断必然成立，因此那条 100 天前的
// 记录可能在写入后就已经被异步删掉了。这样测"PruneAuditLogs 删了几条"
// 就会依赖 goroutine 的调度顺序，是个 flaky 测试。
//
// 所以这里不直接 WriteAudit 写过期数据，而是用 SQL 直接插入——
// 绕过写入路径的顺带清理，精确验证 PruneAuditLogs 本身的行为。
func TestPruneAuditLogs(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	ctx := context.Background()

	insertRaw := func(id string, at time.Time) {
		t.Helper()
		if _, err := db.db.ExecContext(ctx,
			`INSERT INTO audit_logs (id, user_id, username, action, result, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`,
			id, "u1", "admin", audit.ActionUserCreate, audit.ResultSuccess, at.Unix()); err != nil {
			t.Fatalf("直接插入审计失败: %v", err)
		}
	}

	insertRaw("old", now.AddDate(0, 0, -100)) // 应被清理
	insertRaw("edge", now.AddDate(0, 0, -89)) // 应保留
	insertRaw("today", now)                   // 应保留

	n, err := db.PruneAuditLogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("应删除 1 条过期记录，实际 %d", n)
	}

	have, err := db.CountAuditLogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if have != 2 {
		t.Errorf("应剩 2 条，实际 %d", have)
	}

	// 逐条确认剩下的确实是"未过期"的那些，而不只是数量对。
	list, _, err := db.ListAuditLogs(ctx, AuditFilters{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		if r.ID == "old" {
			t.Error("100 天前的记录应被清理")
		}
	}
}

// TestWriteAuditTriggersPrune 验证写入时会顺带清理过期记录。
//
// 这是保留策略的"自动"部分：管理员不需要记得手动清理。
// 因为清理是异步的，这里轮询等待而不是固定 sleep——
// 固定 sleep 要么白等要么在大负载机器上偶发失败。
func TestWriteAuditTriggersPrune(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 直接插入一条过期记录，避免走 WriteAudit 的顺带清理。
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO audit_logs (id, user_id, username, action, result, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		"stale", "u1", "admin", audit.ActionUserCreate, audit.ResultSuccess,
		now.AddDate(0, 0, -120).Unix()); err != nil {
		t.Fatal(err)
	}

	// 触发一次写入（首次写入时间隔必然满足，会启动异步清理）。
	if err := db.WriteAudit(mkAudit("u1", audit.ActionUserCreate, now)); err != nil {
		t.Fatal(err)
	}

	// 轮询等待异步清理完成。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n, err := db.CountAuditLogs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 1 { // 只剩刚写入的那条
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("写入后应自动清理掉 120 天前的过期记录")
}

// TestAuditSurvivesDeletedUser 验证用户删除后审计仍可读。
//
// 这是 username 冗余存储的意义所在：审计记录的是"当时发生了什么"，
// 用户被删掉之后「谁删了什么」的答案恰恰最被需要。
// 若靠 JOIN 取名字，这时候会变成一片空白。
func TestAuditSurvivesDeletedUser(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// 直接写审计，不需要真实用户表记录——
	// 这本身就说明审计不依赖用户表存在。
	if err := db.WriteAudit(audit.Record{
		UserID:    "ghost-user",
		Username:  "已删除的管理员",
		Action:    audit.ActionAgentDelete,
		Target:    "a1",
		Result:    audit.ResultSuccess,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	list, _, err := db.ListAuditLogs(ctx, AuditFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("应有 1 条，实际 %d", len(list))
	}
	if list[0].Username != "已删除的管理员" {
		t.Errorf("用户名快照应保留，实际 %q", list[0].Username)
	}
}

// TestListAuditEmptyNoFilter 验证空库查询不报错。
func TestListAuditEmptyNoFilter(t *testing.T) {
	db := newTestDB(t)
	list, total, err := db.ListAuditLogs(context.Background(), AuditFilters{})
	if err != nil {
		t.Fatalf("空库查询不应报错: %v", err)
	}
	if total != 0 || len(list) != 0 {
		t.Errorf("空库应返回 0 条，实际 total=%d len=%d", total, len(list))
	}
}

// TestAuditFilterCombination 验证多条件同时生效（AND 而非 OR）。
func TestAuditFilterCombination(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().UTC()
	ctx := context.Background()

	// 只有最后一条同时满足 three 个条件。
	for i := 0; i < 3; i++ {
		rec := mkAudit(fmt.Sprintf("u%d", i), audit.ActionContainerRemove, now)
		rec.AgentID = fmt.Sprintf("a%d", i)
		if err := db.WriteAudit(rec); err != nil {
			t.Fatal(err)
		}
	}

	_, total, err := db.ListAuditLogs(ctx, AuditFilters{
		UserID: "u1", AgentID: "a1", Action: audit.ActionContainerRemove,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("多条件应取交集，实际 total=%d", total)
	}
}
