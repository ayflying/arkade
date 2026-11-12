package store

import (
	"arkade/hub/internal/dbmigrate"
	"arkade/hub/internal/testdb"
	"context"
	"testing"
)

func TestFreshPostgresMigrationV2(t *testing.T) {
	db := newTestDB(t)
	var version int
	var dirty bool
	if err := rawDBOf(t, db).QueryRowContext(context.Background(), `SELECT version, dirty FROM `+dbmigrate.MigrationsTable).Scan(&version, &dirty); err != nil {
		t.Fatal(err)
	}
	if version != 2 || dirty {
		t.Fatalf("migration cursor version=%d dirty=%v", version, dirty)
	}
	var n int
	if err := rawDBOf(t, db).QueryRow(`SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name='schema_migrations'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("legacy migration table exists")
	}
}

func TestComposeStacksSchema(t *testing.T) {
	db := newTestDB(t)
	rows, err := rawDBOf(t, db).Query(`SELECT column_name,is_nullable,column_default FROM information_schema.columns WHERE table_schema='public' AND table_name='compose_stacks'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	var versionDefault string
	for rows.Next() {
		var name, nullable string
		var def *string
		if err := rows.Scan(&name, &nullable, &def); err != nil {
			t.Fatal(err)
		}
		got[name] = nullable
		if name == "version" && def != nil {
			versionDefault = *def
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, col := range []string{"id", "agent_id", "name", "content", "env_content", "config_hash", "version", "created_at", "updated_at"} {
		if got[col] != "NO" {
			t.Errorf("%s missing or nullable: %q", col, got[col])
		}
	}
	if versionDefault != "1" {
		t.Errorf("version default %q", versionDefault)
	}
	var unique bool
	if err := rawDBOf(t, db).QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE schemaname='public' AND tablename='compose_stacks' AND indexdef LIKE 'CREATE UNIQUE INDEX% (agent_id, name)%')`).Scan(&unique); err != nil {
		t.Fatal(err)
	}
	if !unique {
		t.Fatal("missing (agent_id,name) unique index")
	}
}

func TestMigrationIdempotent(t *testing.T) {
	dsn := testdb.New(t)
	for i := 0; i < 3; i++ {
		db, err := Open(context.Background(), dsn)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
