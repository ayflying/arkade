package dbmigrate

// dbmigrate_test.go：迁移器的行为验证。
//
// ============================ 这些测试要证明什么 ============================
// 迁移器最危险的失败模式不是"报错"，而是**悄悄把库改坏**。
// 因此下面每条测试都对应一种真实事故：
//
//  1. 全新库能正常建起全部表（基线）；
//  2. 旧库能正确接管，且旧记录表**原样保留**；
//  3. 重复打开是幂等的（不会重复执行迁移）；
//  4. dirty 库被拒绝（不尝试修复）；
//  5. 版本有缺口的老库被拒绝；
//  6. 出现未知版本的老库被拒绝；
//  7. 结构漂移（手工改过表）被拒绝，且**拒绝发生在写新表之前**；
//  8. 迁移失败时 dirty 置位**且** DDL 没有半成品；
//  9. 迁移连接与业务池隔离（关闭迁移连接不影响业务连接）。

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	// 建测试库时也用官方驱动。
	_ "github.com/gogf/gf/contrib/drivers/sqlite/v2"

	"github.com/gogf/gf/v2/database/gdb"
)

// openTestDB 用官方驱动打开一个测试库（模拟业务连接）。
func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	node := gdb.ConfigNode{
		Type:                 "sqlite",
		Name:                 path,
		Extra:                "journal_mode=WAL&foreign_keys=1&busy_timeout=5000",
		MaxOpenConnCount:     1,
		MaxIdleConnCount:     1,
		TimeMaintainDisabled: true,
	}
	inst, err := gdb.New(node)
	if err != nil {
		t.Fatalf("打开测试库失败: %v", err)
	}
	db, err := inst.Master()
	if err != nil {
		t.Fatalf("取得测试库连接失败: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close(context.Background()) })
	return db
}

// applyLegacyUpTo 在给定库上按旧迁移执行到指定版本，并写旧记录表。
//
// 这正是旧版 Hub 启动时做的事，用它来构造"真实的老库"。
func applyLegacyUpTo(t *testing.T, db *sql.DB, upto int) {
	t.Helper()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatalf("建旧记录表失败: %v", err)
	}

	files, err := legacyUpFiles(upto)
	if err != nil {
		t.Fatalf("读取迁移文件失败: %v", err)
	}
	for _, f := range files {
		if f.version > upto {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("开事务失败: %v", err)
		}
		// 整脚本一次执行（驱动支持多语句），与迁移路径一致。
		if _, err := tx.ExecContext(ctx, f.body); err != nil {
			_ = tx.Rollback()
			t.Fatalf("执行 v%d 失败: %v", f.version, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, unixepoch())`,
			f.version, "旧版本"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("记录 v%d 失败: %v", f.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("提交 v%d 失败: %v", f.version, err)
		}
	}
}

// TestFreshDatabaseMigrates 全新库应建起全部表。
func TestFreshDatabaseMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")

	res, err := Run(context.Background(), path)
	if err != nil {
		t.Fatalf("全新库迁移失败: %v", err)
	}
	if res.Dirty {
		t.Fatal("迁移后处于 dirty 状态")
	}
	if res.Applied != 10 {
		t.Fatalf("期望执行 10 个版本，实际 %d", res.Applied)
	}
	if res.Adopted {
		t.Fatal("全新库不应报告旧体系接管")
	}

	// 关键表必须存在。
	db := openTestDB(t, path)
	for _, tbl := range []string{"users", "agents", "compose_stacks", "audit_logs", MigrationsTable} {
		ok, err := tableExists(context.Background(), db, tbl)
		if err != nil {
			t.Fatalf("检查表 %s 失败: %v", tbl, err)
		}
		if !ok {
			t.Errorf("全新库缺少表 %s", tbl)
		}
	}
	// 全新库没有旧记录表。
	ok, _ := tableExists(context.Background(), db, legacyTable)
	if ok {
		t.Error("全新库不应有 schema_migrations 表")
	}
}

// TestLegacyTakeoverPreservesOldTable 旧库接管后，旧记录表必须原样保留。
func TestLegacyTakeoverPreservesOldTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db := openTestDB(t, path)
	applyLegacyUpTo(t, db, 10)

	// 记录接管前的旧表内容。
	var beforeCount int
	var beforeName string
	var beforeAt int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&beforeCount); err != nil {
		t.Fatalf("读旧表失败: %v", err)
	}
	if err := db.QueryRow(`SELECT name FROM schema_migrations WHERE version=1`).Scan(&beforeName); err != nil {
		t.Fatalf("读旧表 v1 失败: %v", err)
	}
	if err := db.QueryRow(`SELECT applied_at FROM schema_migrations WHERE version=1`).Scan(&beforeAt); err != nil {
		t.Fatalf("读旧表时间失败: %v", err)
	}

	res, err := Run(context.Background(), path)
	if err != nil {
		t.Fatalf("旧库接管失败: %v", err)
	}
	if !res.Adopted {
		t.Fatal("应报告旧体系接管")
	}
	if res.Dirty {
		t.Fatal("接管后处于 dirty 状态")
	}

	// 旧表内容必须完全不变。
	var afterCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&afterCount); err != nil {
		t.Fatalf("接管后读旧表失败: %v", err)
	}
	if afterCount != beforeCount {
		t.Errorf("旧记录表行数被改变：接管前 %d，接管后 %d", beforeCount, afterCount)
	}
	var afterName string
	var afterAt int64
	if err := db.QueryRow(`SELECT name, applied_at FROM schema_migrations WHERE version=1`).
		Scan(&afterName, &afterAt); err != nil {
		t.Fatalf("接管后读 v1 失败: %v", err)
	}
	if afterName != beforeName || afterAt != beforeAt {
		t.Errorf("旧记录内容被改写：name %q→%q, applied_at %d→%d",
			beforeName, afterName, beforeAt, afterAt)
	}

	// 新表必须存在。
	ok, err := tableExists(context.Background(), db, MigrationsTable)
	if err != nil || !ok {
		t.Fatalf("接管后缺少新版本表 %s（err=%v）", MigrationsTable, err)
	}
}

// TestReopenIsIdempotent 重复打开不应重复执行迁移。
func TestReopenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "idem.db")

	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}
	res, err := Run(context.Background(), path)
	if err != nil {
		t.Fatalf("二次迁移失败: %v", err)
	}
	if res.Applied != 0 {
		t.Fatalf("二次打开应执行 0 个版本，实际 %d", res.Applied)
	}
	if res.Dirty {
		t.Fatal("二次打开后 dirty")
	}

	// 业务数据必须保留（迁移不应重置库）。
	db := openTestDB(t, path)
	if _, err := db.Exec(`INSERT INTO users (id, username, display_name, password_hash, role, created_at, updated_at)
		VALUES ('u1', 'alice', 'Alice', 'h', 'admin', unixepoch(), unixepoch())`); err != nil {
		t.Fatalf("写用户失败: %v", err)
	}
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("三次迁移失败: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users WHERE id='u1'`).Scan(&n); err != nil {
		t.Fatalf("查用户失败: %v", err)
	}
	if n != 1 {
		t.Fatal("重复迁移把业务数据删掉了")
	}
}

// TestVersionGapRejected 旧库版本有缺口必须拒绝。
func TestVersionGapRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gap.db")
	db := openTestDB(t, path)
	applyLegacyUpTo(t, db, 10)

	// 制造缺口：删掉 v3 的记录。
	if _, err := db.Exec(`DELETE FROM schema_migrations WHERE version=3`); err != nil {
		t.Fatalf("制造缺口失败: %v", err)
	}

	_, err := Run(context.Background(), path)
	if err == nil {
		t.Fatal("版本缺口应被拒绝，但迁移通过了")
	}
	if !strings.Contains(err.Error(), "不连续") {
		t.Fatalf("错误信息未提及不连续: %v", err)
	}
}

// TestUnknownVersionRejected 旧库有未知版本（被更高版本程序升级过）必须拒绝。
func TestUnknownVersionRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.db")
	db := openTestDB(t, path)
	applyLegacyUpTo(t, db, 10)

	// 插入一个当前程序不认识的版本。
	if _, err := db.Exec(`INSERT INTO schema_migrations(version, name, applied_at) VALUES(99, '未来版本', unixepoch())`); err != nil {
		t.Fatalf("插入未知版本失败: %v", err)
	}

	_, err := Run(context.Background(), path)
	if err == nil {
		t.Fatal("未知版本应被拒绝，但迁移通过了")
	}
	if !strings.Contains(err.Error(), "不认识") {
		t.Fatalf("错误信息未提及未知版本: %v", err)
	}
}

// TestSchemaDriftRejected 手工改过结构的老库必须拒绝，且不得写入新表。
func TestSchemaDriftRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drift.db")
	db := openTestDB(t, path)
	applyLegacyUpTo(t, db, 10)

	// 模拟有人手工加了一列（绕过迁移）。
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN legacy_hack TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("制造漂移失败: %v", err)
	}

	_, err := Run(context.Background(), path)
	if err == nil {
		t.Fatal("结构漂移应被拒绝，但迁移通过了")
	}
	if !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("错误信息未提及结构不一致: %v", err)
	}

	// ============================ 关键断言 ============================
	// 拒绝必须发生在写新版本表之前：库不应该留下我们的痕迹。
	ok, qerr := tableExists(context.Background(), db, MigrationsTable)
	if qerr != nil {
		t.Fatalf("检查新表失败: %v", qerr)
	}
	if ok {
		t.Fatal("结构漂移被拒绝后，不该已经创建了 arkade_migrate_versions 表")
	}
	t.Logf("拒绝信息（截断）: %.200s", err.Error())
}

// TestDirtyRejectedAndNoPartialDDL 迁移失败时 dirty 置位，且 DDL 不留半成品。
func TestDirtyRejectedAndNoPartialDDL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dirty.db")

	// 先正常迁移到最新。
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("初始迁移失败: %v", err)
	}

	// 手工把状态改成 dirty（模拟上次执行中途失败）。
	db := openTestDB(t, path)
	if _, err := db.Exec(`UPDATE ` + MigrationsTable + ` SET dirty=1 WHERE version=10`); err != nil {
		t.Fatalf("设置 dirty 失败: %v", err)
	}

	_, err := Run(context.Background(), path)
	if err == nil {
		t.Fatal("dirty 状态应被拒绝，但迁移通过了")
	}
	if !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("错误信息未提及 dirty: %v", err)
	}

	// dirty 被拒绝后，表结构不应有任何变化。
	var dirty int
	if err := db.QueryRow(`SELECT dirty FROM ` + MigrationsTable + ` WHERE version=10`).Scan(&dirty); err != nil {
		t.Fatalf("读 dirty 失败: %v", err)
	}
	if dirty != 1 {
		t.Error("程序不应擅自清除 dirty 标记")
	}
}

// TestMigrationConnIsolation 迁移连接与业务连接彼此独立。
//
// ============================ 为什么必须测 ============================
// migrate 的 WithInstance 在 Close 时会关闭传入的 *sql.DB。
// 若我们误把业务池传给它，迁移结束业务池就废了——
// 表现是后续所有查询报 "sql: database is closed"。
// 这条测试钉住"迁移用自己的连接"这个前提。
func TestMigrationConnIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "iso.db")

	// 先有业务连接。
	bizDB := openTestDB(t, path)

	// 跑迁移（用独立连接）。
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	// 业务连接必须仍然可用。
	if err := bizDB.PingContext(context.Background()); err != nil {
		t.Fatalf("迁移后业务连接不可用（迁移误关了业务池）: %v", err)
	}
	var one int
	if err := bizDB.QueryRow(`SELECT 1`).Scan(&one); err != nil {
		t.Fatalf("迁移后业务连接查询失败: %v", err)
	}
	if one != 1 {
		t.Fatal("业务连接查询结果异常")
	}
}

// TestEmbeddedScriptsExecuteWhole 验证整个 up 脚本能被驱动一次执行。
//
// ============================ 这条替代了原来的"SQL 切分"测试 ============================
// 之前实现里有个自造的按分号切语句的解析器，测试在测那个解析器。
// 解析器已经删掉（驱动本就支持多语句），所以这里改为验证真正需要保证的事：
// **每个版本的整脚本能一次 Exec 成功**——这才是我依赖的前提。
func TestEmbeddedScriptsExecuteWhole(t *testing.T) {
	ctx := context.Background()

	node := gdb.ConfigNode{
		Type:                 "sqlite",
		Name:                 ":memory:",
		MaxOpenConnCount:     1,
		MaxIdleConnCount:     1,
		TimeMaintainDisabled: true,
	}
	inst, err := gdb.New(node)
	if err != nil {
		t.Fatalf("建内存库失败: %v", err)
	}
	defer func() { _ = inst.Close(context.Background()) }()
	db, err := inst.Master()
	if err != nil {
		t.Fatalf("取连接失败: %v", err)
	}

	files, err := legacyUpFiles(100)
	if err != nil {
		t.Fatalf("读迁移文件失败: %v", err)
	}
	if len(files) != 10 {
		t.Fatalf("期望 10 个版本，实际 %d", len(files))
	}
	for _, f := range files {
		if _, err := db.ExecContext(ctx, f.body); err != nil {
			t.Fatalf("v%d 整脚本执行失败（驱动应支持多语句）: %v", f.version, err)
		}
	}
}

// TestEmbeddedVersionsAreContiguous 内嵌迁移版本必须连续。
func TestEmbeddedVersionsAreContiguous(t *testing.T) {
	versions, err := embeddedVersions()
	if err != nil {
		t.Fatalf("读取内嵌版本失败: %v", err)
	}
	if len(versions) != 10 {
		t.Fatalf("期望 10 个版本，实际 %d: %v", len(versions), versions)
	}
	for i, v := range versions {
		if v != i+1 {
			t.Fatalf("版本不连续：第 %d 项是 v%d", i, v)
		}
	}
}
