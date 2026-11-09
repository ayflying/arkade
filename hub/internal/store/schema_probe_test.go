package store

// schema_probe_test.go 是迁移结构的守卫测试。
//
// 目的：迁移只追加不修改的规则，靠人记不住。这里把「v2 必须存在且表结构符合预期」
// 固定成断言——将来有人误改 v1 语句或漏加索引，这里会立刻报出来。

import (
	"context"
	"path/filepath"
	"testing"
)

// TestMigrationV2Applied 验证全新库上 v1 与 v2 都被应用。
func TestMigrationV2Applied(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "m.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.db.QueryContext(context.Background(),
		`SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		t.Fatalf("查询迁移记录失败: %v", err)
	}
	defer rows.Close()

	var versions []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, v)
	}
	if len(versions) < 2 {
		t.Fatalf("应至少有 v1 与 v2，实际 %v", versions)
	}
	if versions[0] != 1 || versions[1] != 2 {
		t.Errorf("迁移版本应为 [1 2 ...]，实际 %v", versions)
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
	rows, err := db.db.QueryContext(context.Background(), `PRAGMA table_info(compose_stacks)`)
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
	irows, err := db.db.QueryContext(context.Background(), `PRAGMA index_list(compose_stacks)`)
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
