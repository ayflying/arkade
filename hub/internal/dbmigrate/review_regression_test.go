package dbmigrate

// 独立 review 回归：验证迁移器不会把“看似成功”当成数据安全。

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

func TestReviewEveryLegacyVersionUpgradesAndReopens(t *testing.T) {
	for version := 1; version <= 10; version++ {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "legacy.db")
			db := openTestDB(t, path)
			applyLegacyUpTo(t, db, version)
			if _, err := db.Exec(`INSERT INTO users(id, username, display_name, password_hash, role, disabled, created_at, updated_at) VALUES('kept-user', 'kept-user', '原有用户', 'keep-hash', 'admin', 0, 123, 456)`); err != nil {
				t.Fatal(err)
			}
			result, err := Run(ctx, path)
			if err != nil || result.ToVersion != latestTestVersion(t) || result.Dirty {
				t.Fatalf("upgrade v%d: result=%+v err=%v", version, result, err)
			}
			var name, hash string
			var created, updated int64
			if err := db.QueryRow(`SELECT display_name,password_hash,created_at,updated_at FROM users WHERE id='kept-user'`).Scan(&name, &hash, &created, &updated); err != nil {
				t.Fatal(err)
			}
			if name != "原有用户" || hash != "keep-hash" || created != 123 || updated != 456 {
				t.Fatalf("upgrade changed existing user: %q %q %d %d", name, hash, created, updated)
			}
			result, err = Run(ctx, path)
			if err != nil || result.Applied != 0 || result.ToVersion != latestTestVersion(t) {
				t.Fatalf("reopen v%d: result=%+v err=%v", version, result, err)
			}
		})
	}
}

func TestReviewSchemaComparisonPreservesLiteralSemantics(t *testing.T) {
	for _, pair := range [][2]string{
		{"CREATE TABLE t (v TEXT DEFAULT 'ABC')", "CREATE TABLE t (v TEXT DEFAULT 'abc')"},
		{"CREATE TABLE t (v TEXT DEFAULT 'a  b')", "CREATE TABLE t (v TEXT DEFAULT 'a b')"},
	} {
		if normalizeSQL(pair[0]) == normalizeSQL(pair[1]) {
			t.Fatalf("结构归一化抹掉了字符串字面量差异: %q / %q", pair[0], pair[1])
		}
	}
}

func TestReviewRejectUnversionedBusinessDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unversioned.db")
	db := openTestDB(t, path)
	if _, err := db.Exec(`CREATE TABLE important_data(id TEXT PRIMARY KEY, value TEXT); INSERT INTO important_data VALUES ('original', 'keep')`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), path); err == nil {
		t.Fatal("存在业务表但没有任何版本记录的数据库不应被自动当成空库接管")
	}
	var value string
	if err := db.QueryRow(`SELECT value FROM important_data WHERE id='original'`).Scan(&value); err != nil || value != "keep" {
		t.Fatalf("拒绝接管后数据发生变化: value=%q err=%v", value, err)
	}
	if exists, err := tableExists(context.Background(), db, MigrationsTable); err != nil || exists {
		t.Fatalf("拒绝接管之前不应写入新版本表: exists=%v err=%v", exists, err)
	}
}

func TestReviewUnknownNewVersionRejectedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "future.db")
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t, path)
	if _, err := db.Exec(`UPDATE arkade_migrate_versions SET version=999, dirty=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), path); err == nil {
		t.Fatal("新体系记录未知更高版本时必须拒绝启动")
	}
	var version, dirty int
	if err := db.QueryRow(`SELECT version, dirty FROM arkade_migrate_versions`).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 999 || dirty != 0 {
		t.Fatalf("拒绝未知版本时不得修改游标: version=%d dirty=%d", version, dirty)
	}
}

func TestReviewRejectMultipleManagedVersionRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "multiple-versions.db")
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	db := openTestDB(t, path)
	if _, err := db.Exec(`INSERT INTO arkade_migrate_versions(version, dirty) VALUES(1, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), path); err == nil {
		t.Fatal("新版本表存在两行游标必须拒绝，不能 LIMIT 1 任取一行")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM arkade_migrate_versions`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("拒绝多行游标时不得清理或重写现场: count=%d err=%v", count, err)
	}
}

func TestReviewUnsupportedDownCannotSilentlyRewind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-down.db")
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	conn, err := openMigrationConn(path)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.close()
	m, err := newMigrator(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = m.Close() }()
	before, _, err := m.Version()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Steps(-1); err == nil {
		t.Fatal("历史版本没有安全 down 时必须报错，不能执行空脚本假装回滚")
	}
	after, dirty, err := m.Version()
	if err != nil || after != before || dirty {
		t.Fatalf("不支持的 down 不应倒退或弄脏版本: before=%d after=%d dirty=%v err=%v", before, after, dirty, err)
	}
}
