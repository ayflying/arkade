package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestComposeEnvCreateReadList(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"alpha", "beta"} {
		s := mkStack("a1", name, "services: {}\n")
		if name == "alpha" {
			s.EnvContent = "# exact bytes\r\nTOKEN='你好=world'\r\nEMPTY=\r\n"
		}
		if err := db.CreateComposeStack(ctx, s); err != nil {
			t.Fatal(err)
		}
		got, err := db.GetComposeStack(ctx, s.AgentID, s.Name)
		if err != nil {
			t.Fatal(err)
		}
		if got.EnvContent != s.EnvContent || got.Content != s.Content || got.ConfigHash != s.ConfigHash {
			t.Fatalf("create/read lost project content: %+v", got)
		}
	}
	list, err := db.ListComposeStacks(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "alpha" || list[0].EnvContent != "# exact bytes\r\nTOKEN='你好=world'\r\nEMPTY=\r\n" || list[1].EnvContent != "" {
		t.Fatalf("list lost env content/order: %+v", list)
	}
}

func TestUpdateComposeProjectOptimisticEnv(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	s := mkStack("a1", "web", "original")
	s.EnvContent = "OLD=1\n"
	s.SyncedAt = time.Unix(100, 0).UTC()
	if err := db.CreateComposeStack(ctx, s); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(200, 0).UTC()
	updated, ok, err := db.UpdateComposeProject(ctx, "a1", "web", "new compose", "NEW=2\n", "project-hash", 1, at)
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if updated.Content != "new compose" || updated.EnvContent != "NEW=2\n" || updated.ConfigHash != "project-hash" || updated.Version != 2 || !updated.UpdatedAt.Equal(at) || !updated.SyncedAt.Equal(s.SyncedAt) {
		t.Fatalf("incomplete project update: %+v", updated)
	}
	stale, ok, err := db.UpdateComposeProject(ctx, "a1", "web", "stale", "STALE=1", "stale-hash", 1, at.Add(time.Hour))
	if err != nil || ok || stale != (ComposeStack{}) {
		t.Fatalf("stale update: stack=%+v ok=%v err=%v", stale, ok, err)
	}
	got, err := db.GetComposeStack(ctx, "a1", "web")
	if err != nil || !reflect.DeepEqual(got, updated) {
		t.Fatalf("stale update changed row: %+v err=%v", got, err)
	}
	// Explicit empty env removes the previous file contents, without losing compose.
	cleared, ok, err := db.UpdateComposeProject(ctx, "a1", "web", updated.Content, "", "cleared-hash", 2, at.Add(time.Minute))
	if err != nil || !ok || cleared.EnvContent != "" || cleared.Version != 3 || cleared.Content != updated.Content {
		t.Fatalf("clear env: stack=%+v ok=%v err=%v", cleared, ok, err)
	}
	_, ok, err = db.UpdateComposeProject(ctx, "a1", "missing", "c", "e", "h", 1, at)
	if ok || !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing project: ok=%v err=%v", ok, err)
	}
}

func TestUpdateComposeStackPreservesEnv(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	s := mkStack("a1", "web", "old")
	s.EnvContent = "SECRET=keep\n"
	if err := db.CreateComposeStack(ctx, s); err != nil {
		t.Fatal(err)
	}
	updated, ok, err := db.UpdateComposeStack(ctx, "a1", "web", 1, "new", "new-hash")
	if err != nil || !ok || updated.EnvContent != s.EnvContent || updated.Version != 2 {
		t.Fatalf("legacy update: %+v ok=%v err=%v", updated, ok, err)
	}
	_, ok, err = db.UpdateComposeStack(ctx, "a1", "web", 1, "stale", "stale-hash")
	if err != nil || ok {
		t.Fatalf("legacy stale update: ok=%v err=%v", ok, err)
	}
	got, err := db.GetComposeStack(ctx, "a1", "web")
	if err != nil || !reflect.DeepEqual(got, updated) {
		t.Fatalf("legacy stale update changed row: %+v err=%v", got, err)
	}
}

func TestComposeEnvV7Migration(t *testing.T) {
	ctx := context.Background()
	// 本测试验证 legacy 迁移器（migrate/applyMigration，仅测试保留）产出的行内容。
	// 必须从一个**未迁移**的库开始，所以不能用 Open（它会先跑完 dbmigrate）。
	// 这里用裸连接自建库，再包一个只带 gdb 引用的实例供 legacy 迁移器使用。
	path := filepath.Join(t.TempDir(), "legacy_env.db")
	inst, err := openGDB(path)
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	db := &DB{gdb: inst}
	defer inst.Close(context.Background())
	raw := rawDBOf(t, db)
	if _, err := raw.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations {
		if m.version < 7 {
			if err := db.applyMigration(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
	}
	// A pre-v7 row must migrate with an empty non-null environment.
	if _, err := raw.ExecContext(ctx, `INSERT INTO compose_stacks (id, agent_id, name, content, config_hash, created_at, updated_at) VALUES ('old', 'a1', 'web', 'old-compose', 'old-hash', 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(ctx); err != nil {
		t.Fatalf("migration is not idempotent: %v", err)
	}
	// 这里直接用原生 SQL 读，不走 GetComposeStack：本测试验证的是 legacy
	// 迁移器产出的行内容，而本文件的 DB 是手工构造的（只有 db，没有 gdb），
	// 无法走 ORM 路径。用 SQL 读反而更贴近"迁移后磁盘上到底是什么"。
	var (
		content, env, hash string
		version            int64
	)
	if err := raw.QueryRowContext(ctx,
		`SELECT content, env_content, config_hash, version FROM compose_stacks WHERE agent_id = ? AND name = ?`,
		"a1", "web").Scan(&content, &env, &hash, &version); err != nil {
		t.Fatalf("读取迁移后的行失败: %v", err)
	}
	if env != "" || content != "old-compose" || version != 1 {
		t.Fatalf("migrated row: content=%q env=%q hash=%q version=%d", content, env, hash, version)
	}
	if _, err := raw.ExecContext(ctx, `UPDATE compose_stacks SET env_content = NULL WHERE id = 'old'`); err == nil {
		t.Fatal("env_content must be NOT NULL")
	}
}
