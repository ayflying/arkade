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

// TestAuditRetentionPolicyIsThirtyDays 锁住"审计最多保存 30 天"这条产品约束。
//
// 它同时是**上限**与默认值：默认值写在初始迁移的种子数据里，
// 上限由 settings 服务校验，两者必须一致，否则新实例建出来就超出上限。
func TestAuditRetentionPolicyIsThirtyDays(t *testing.T) {
	if AuditRetentionDays != 30 {
		t.Fatalf("审计保留上限应为 30 天，实际 %d", AuditRetentionDays)
	}
	if AuditMinRetentionDays != 1 {
		t.Fatalf("审计保留下限应为 1 天，实际 %d", AuditMinRetentionDays)
	}
	if AuditMinRetentionDays > AuditRetentionDays {
		t.Fatal("保留期下限不能大于上限")
	}
}

// TestClampAuditRetention 验证存量超限配置在打开库时被收敛到 30 天。
//
// 回归背景：升级前默认 90 天、上限 3650 天，仅改常量的话旧库那一行
// 仍是 90，管理员不会再去点一次保存，"最多 30 天"对存量实例不生效。
// 同时必须保留管理员的合法选择（例如 7 天），不能一律改成 30。
func TestClampAuditRetention(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	set := func(days int) {
		t.Helper()
		if _, err := rawDBOf(t, db).ExecContext(ctx,
			`UPDATE system_settings SET audit_retention_days = $1 WHERE id = 1`, days); err != nil {
			t.Fatalf("改保留天数失败: %v", err)
		}
	}
	get := func() int {
		t.Helper()
		var days int
		if err := rawDBOf(t, db).QueryRow(`SELECT audit_retention_days FROM system_settings WHERE id = 1`).Scan(&days); err != nil {
			t.Fatalf("读保留天数失败: %v", err)
		}
		return days
	}

	// 超上限的存量值（旧默认 90、旧上限内被调大的 3650）→ 收到 30。
	for _, over := range []int{90, 3650} {
		set(over)
		if err := db.clampAuditRetention(ctx); err != nil {
			t.Fatal(err)
		}
		if got := get(); got != AuditRetentionDays {
			t.Fatalf("%d 天应收敛到 %d，实际 %d", over, AuditRetentionDays, got)
		}
	}

	// 合法值原样保留：7 天是管理员的真实选择。
	set(7)
	if err := db.clampAuditRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != 7 {
		t.Fatalf("合法的 7 天被改成了 %d", got)
	}

	// 非法的 0 天 → 抬到上限（不能继续用 0，那等于写完即删）。
	set(0)
	if err := db.clampAuditRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != AuditRetentionDays {
		t.Fatalf("0 天应收敛到 %d，实际 %d", AuditRetentionDays, got)
	}

	// 幂等：已经合规时再跑一次不改变结果。
	if err := db.clampAuditRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != AuditRetentionDays {
		t.Fatalf("重复收敛改变了结果: %d", got)
	}
}

// Only the configured retention worker may expire historical records.
func TestWriteAuditDoesNotPrune(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// 直接插入一条过期记录，避免走 WriteAudit 的顺带清理。
	if _, err := rawDBOf(t, db).ExecContext(ctx,
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

	n, err := db.CountAuditLogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("写入不应隐式清理旧记录，实际 %d", n)
	}
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
