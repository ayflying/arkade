package store

// schema_probe_test.go 是迁移结构的守卫测试。
//
// 目的：迁移只追加不修改的规则，靠人记不住。这里把「v2 必须存在且表结构符合预期」
// 固定成断言——将来有人误改 v1 语句或漏加索引，这里会立刻报出来。

import (
	"context"
	"path/filepath"
	"testing"

	"arkade/hub/internal/dbmigrate"
)

// TestMigrationV2Applied 验证全新库上的迁移记录正确。
//
// ============================ 注意表名变了 ============================
// 迁移体系从自建 schema_migrations 换成 golang-migrate 的
// arkade_migrate_versions 后，全新库**不再有** schema_migrations 表
// （它是旧体系的记录表，只在被接管的老库里保留）。
//
// 新表是"单行游标"结构（version + dirty），不是"每版本一行"，
// 所以这里断言的是"游标已推进到最新版本且不脏"，
// 而不是旧测试那样的"版本列表是 [1 2 3 ...]"。
func TestMigrationV2Applied(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	var version int
	var dirty int
	if err := rawDBOf(t, db).QueryRowContext(context.Background(),
		`SELECT version, dirty FROM `+dbmigrate.MigrationsTable).Scan(&version, &dirty); err != nil {
		t.Fatalf("查询迁移游标失败: %v", err)
	}
	if dirty != 0 {
		t.Fatalf("全新库迁移后不应处于 dirty 状态")
	}
	// 期望已推进到最新版本（当前共 10 个）。
	if version != 10 {
		t.Errorf("迁移游标应推进到 v10，实际 v%d", version)
	}

	// 全新库不应存在旧体系记录表。
	var n int
	if err := rawDBOf(t, db).QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&n); err != nil {
		t.Fatalf("检查旧表失败: %v", err)
	}
	if n != 0 {
		t.Error("全新库不应创建旧体系的 schema_migrations 表")
	}
}

// TestComposeStacksSchema 验证 compose_stacks 的关键约束。
func TestComposeStacksSchema(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	// 列齐全性与 NOT NULL 约束。
	wantCols := map[string]bool{ // 列名 → 是否必须 NOT NULL
		"id": true, "agent_id": true, "name": true, "content": true,
		"config_hash": true, "version": true, "created_at": true, "updated_at": true,
	}
	rows, err := rawDBOf(t, db).QueryContext(context.Background(), `PRAGMA table_info(compose_stacks)`)
	if err != nil {
		t.Fatalf("读取表结构失败: %v", err)
	}
	got := map[string]struct {
		notnull bool
		dflt    string
	}{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		d := ""
		if dflt != nil {
			d = *dflt
		}
		got[name] = struct {
			notnull bool
			dflt    string
		}{notnull: notnull == 1, dflt: d}
	}
	rows.Close()

	for col, wantNotNull := range wantCols {
		g, ok := got[col]
		if !ok {
			t.Errorf("缺少列 %s", col)
			continue
		}
		if wantNotNull && !g.notnull {
			t.Errorf("列 %s 应为 NOT NULL", col)
		}
	}
	// version 必须有默认值 1，否则插入时漏写会变成 NULL。
	if g, ok := got["version"]; ok && g.dflt != "1" {
		t.Errorf("version 默认值应为 1，实际 %q", g.dflt)
	}

	// 唯一索引必须存在：同 Agent 下项目名唯一是本表的核心约束。
	irows, err := rawDBOf(t, db).QueryContext(context.Background(), `PRAGMA index_list(compose_stacks)`)
	if err != nil {
		t.Fatalf("读取索引失败: %v", err)
	}
	defer irows.Close()
	hasUnique := false
	for irows.Next() {
		var seq, uniq, partial int
		var name, origin string
		if err := irows.Scan(&seq, &name, &uniq, &origin, &partial); err != nil {
			t.Fatal(err)
		}
		if uniq == 1 && name == "idx_compose_stacks_agent_name" {
			hasUnique = true
		}
	}
	if !hasUnique {
		t.Error("缺少 (agent_id, name) 唯一索引")
	}
}

// TestMigrationIdempotent 验证重复打开同一个库不会重复执行迁移。
func TestMigrationIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 3; i++ {
		db, err := Open(context.Background(), path)
		if err != nil {
			t.Fatalf("第 %d 次打开失败: %v", i+1, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("关闭失败: %v", err)
		}
	}
}
