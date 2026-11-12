package dbmigrate_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"arkade/hub/internal/dbmigrate"
	"arkade/hub/internal/store"
	"github.com/gogf/gf/v2/database/gdb"
)

// Exercise the public production entry points, never the legacy executor. The
// SQL fixture uses only tables/columns actually present at its historical version.
func TestBusinessDataLegacyUpgradeProductionRunAndOpen(t *testing.T) {
	for _, version := range []int{6, 7, 8, 9} {
		for _, entry := range []string{"Run", "Open"} {
			t.Run(fmt.Sprintf("v%d/%s", version, entry), func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "business.db")
				inst, err := gdb.New(gdb.ConfigNode{Type: "sqlite", Name: path, Extra: "journal_mode=WAL&foreign_keys=1&busy_timeout=5000", MaxOpenConnCount: 1, MaxIdleConnCount: 1, TimeMaintainDisabled: true})
				if err != nil {
					t.Fatal(err)
				}
				defer inst.Close(ctx)
				db, err := inst.Master()
				if err != nil {
					t.Fatal(err)
				}
				businessLegacyFixture(t, db, version)
				before := businessSnapshot(t, db)
				if entry == "Run" {
					result, err := dbmigrate.Run(ctx, path)
					if err != nil || result == nil || !result.Adopted || result.FromVersion != version || result.ToVersion != 11 || result.Applied != 11-version || result.Dirty {
						t.Fatalf("production upgrade: %+v %v", result, err)
					}
				} else {
					opened, err := store.Open(ctx, path)
					if err != nil {
						t.Fatal(err)
					}
					stack, err := opened.GetComposeStack(ctx, "agent-kept", "web")
					if err != nil || stack.ID != "stack-kept" || stack.Version != 7 {
						t.Fatalf("production repository read: %+v %v", stack, err)
					}
					if err := opened.Close(); err != nil {
						t.Fatal(err)
					}
				}
				assertBusinessSnapshot(t, db, before)
				businessAssertDefaults(t, db, version)
				// Operations first exist in v10: insert only after the real upgrade, then
				// exercise both restart paths so idempotency metadata and stack links persist.
				businessExec(t, db, `INSERT INTO compose_operation_requests(agent_id,request_id,name,action,fingerprint,task_id,stack_id,stack_version,config_hash,created_at,finalized,final_error,record_removed) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
					"agent-kept", "request-kept", "web", "up", "fingerprint;literal", "task-kept", "stack-kept", 7, "hash-kept", 777, 1, "fixed failure", 0)
				final := businessSnapshot(t, db)
				result, err := dbmigrate.Run(ctx, path)
				if err != nil || result.Applied != 0 || result.Dirty || result.Adopted {
					t.Fatalf("restart Run: %+v %v", result, err)
				}
				opened, err := store.Open(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				if err := opened.Close(); err != nil {
					t.Fatal(err)
				}
				assertBusinessSnapshot(t, db, final)
				var related int
				err = db.QueryRow(`SELECT count(*) FROM compose_operation_requests o JOIN compose_stacks s ON s.id=o.stack_id AND s.agent_id=o.agent_id AND s.version=o.stack_version JOIN agents a ON a.id=s.agent_id WHERE o.request_id='request-kept'`).Scan(&related)
				if err != nil || related != 1 {
					t.Fatalf("operation/stack/agent relationship lost: %d %v", related, err)
				}
				rows, err := db.Query(`PRAGMA foreign_key_check`)
				if err != nil {
					t.Fatal(err)
				}
				if rows.Next() {
					rows.Close()
					t.Fatal("foreign key violation after upgrade")
				}
				if err := rows.Err(); err != nil {
					t.Fatal(err)
				}
				rows.Close()
			})
		}
	}
}

func businessLegacyFixture(t *testing.T, db *sql.DB, version int) {
	t.Helper()
	businessExec(t, db, `PRAGMA foreign_keys=ON`)
	businessExec(t, db, `CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at INTEGER NOT NULL)`)
	for v := 1; v <= version; v++ {
		name := fmt.Sprintf("%06d_legacy_v%d.up.sql", v, v)
		body, err := dbmigrate.ReadMigration(name)
		if err != nil {
			t.Fatal(err)
		}
		businessExec(t, db, body)
		businessExec(t, db, `INSERT INTO schema_migrations VALUES(?,?,?)`, v, fmt.Sprintf("legacy v%d", v), 1000+v)
	}
	businessExec(t, db, `INSERT INTO users VALUES(?,?,?,?,?,?,?,?)`, "user-kept", "operator", "中文  原样", "$hash$CASE;  spaces", "user", 0, 111, 222)
	businessExec(t, db, `INSERT INTO groups VALUES(?,?,?,?)`, "group-kept", "Operators", "说明;  spacing", 333)
	businessExec(t, db, `INSERT INTO user_groups VALUES(?,?)`, "user-kept", "group-kept")
	businessExec(t, db, `INSERT INTO group_agents VALUES(?,?,?)`, "group-kept", "agent-kept", "manage")
	businessExec(t, db, `INSERT INTO agents(id,name,status,version,runtime,runtime_version,last_seen,labels,created_at,updated_at,display_name,note,disabled,preprovisioned) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"agent-kept", "machine", "offline", "v-old", "docker", "25.0", 444, `{"Case":"A  B;中文"}`, 101, 202, "生产机", "do not alter;  note", 1, 1)
	businessExec(t, db, `INSERT INTO sessions VALUES(?,?,?,?)`, "session-hash-CASE", "user-kept", 123, 4102444800)
	businessExec(t, db, `INSERT INTO agent_tokens VALUES(?,?,?)`, "agent-token-hash-CASE", "agent-kept", 456)
	businessExec(t, db, `INSERT INTO compose_stacks(id,agent_id,name,content,config_hash,version,created_at,updated_at,synced_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		"stack-kept", "agent-kept", "web", "services:\n  web:\n    image: 'keep:tag'\n    command: 'a;  b'\n", "hash-kept", 7, 100, 200, 300)
	businessExec(t, db, `INSERT INTO audit_logs VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, "audit-kept", "user-kept", "operator", "agent-kept", "生产机", "compose.up", "web", "literal;  原样", "failed", "sanitized error", 321, 555)
	if version >= 7 {
		businessExec(t, db, `UPDATE compose_stacks SET env_content=? WHERE id='stack-kept'`, "TOKEN='Case;  secret'\r\nEMPTY=\nUNICODE=秘密\n")
	}
	if version >= 8 {
		businessExec(t, db, `INSERT INTO external_compose_drafts VALUES(?,?,?,?,?,?)`, "agent-kept", "external", "services:\n  x:\n    environment: ['KEY=草稿秘密;  A']\n", "source-hash", 9, 654)
	}
	if version >= 9 {
		businessExec(t, db, `UPDATE agents SET direct_address=? WHERE id='agent-kept'`, "https://private.example:9443/base")
	}
}

// Capture every original column, not only a handpicked subset. New migration
// defaults are tested separately; original secret bytes and timestamps compare exactly.
type businessTableSnapshot struct {
	columns []string
	rows    [][]any
}

func businessSnapshot(t *testing.T, db *sql.DB) map[string]businessTableSnapshot {
	t.Helper()
	out := map[string]businessTableSnapshot{}
	tables, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name NOT IN ('arkade_migrate_versions','arkade_migrate_checksums') ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var name string
		if err := tables.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := tables.Err(); err != nil {
		t.Fatal(err)
	}
	tables.Close()
	for _, name := range names {
		rows, err := db.Query(`SELECT * FROM "` + name + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal(err)
		}
		snap := businessTableSnapshot{columns: columns}
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			snap.rows = append(snap.rows, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		out[name] = snap
	}
	return out
}
func assertBusinessSnapshot(t *testing.T, db *sql.DB, want map[string]businessTableSnapshot) {
	t.Helper()
	for table, snap := range want {
		selection := ""
		for i, column := range snap.columns {
			if i > 0 {
				selection += ","
			}
			selection += `"` + column + `"`
		}
		rows, err := db.Query(`SELECT ` + selection + ` FROM "` + table + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		var got [][]any
		for rows.Next() {
			values := make([]any, len(snap.columns))
			dest := make([]any, len(values))
			for i := range values {
				dest[i] = &values[i]
			}
			if err := rows.Scan(dest...); err != nil {
				t.Fatal(err)
			}
			got = append(got, values)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		// Never print snapshots: they intentionally contain synthetic secrets.
		if !reflect.DeepEqual(snap.rows, got) {
			t.Errorf("table %s original records changed", table)
		}
	}
}
func businessAssertDefaults(t *testing.T, db *sql.DB, version int) {
	t.Helper()
	var env, direct string
	var drafts, ops int
	if err := db.QueryRow(`SELECT env_content FROM compose_stacks WHERE id='stack-kept'`).Scan(&env); err != nil {
		t.Fatal(err)
	}
	if version < 7 && env != "" {
		t.Fatal("new env default not empty")
	}
	if err := db.QueryRow(`SELECT direct_address FROM agents WHERE id='agent-kept'`).Scan(&direct); err != nil {
		t.Fatal(err)
	}
	if version < 9 && direct != "" {
		t.Fatal("new address default not empty")
	}
	if err := db.QueryRow(`SELECT count(*) FROM external_compose_drafts`).Scan(&drafts); err != nil {
		t.Fatal(err)
	}
	expected := 0
	if version >= 8 {
		expected = 1
	}
	if drafts != expected {
		t.Fatal("draft count changed")
	}
	if err := db.QueryRow(`SELECT count(*) FROM compose_operation_requests`).Scan(&ops); err != nil || ops != 0 {
		t.Fatalf("unexpected operation metadata after upgrade: %d %v", ops, err)
	}
	var relations int
	err := db.QueryRow(`SELECT count(*) FROM users u JOIN user_groups ug ON ug.user_id=u.id JOIN groups g ON g.id=ug.group_id JOIN group_agents ga ON ga.group_id=g.id JOIN agents a ON a.id=ga.agent_id JOIN sessions s ON s.user_id=u.id JOIN agent_tokens tok ON tok.agent_id=a.id WHERE ga.permission='manage'`).Scan(&relations)
	if err != nil || relations != 1 {
		t.Fatalf("grants/session/token relationships lost: %d %v", relations, err)
	}
}
func businessExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("fixture SQL failed: %v", err)
	}
}
