package dbmigrate_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"arkade/hub/internal/dbmigrate"
	"arkade/hub/internal/testdb"
)

func open(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
func execute(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatal(err)
	}
}
func TestDSNErrorsDoNotLeakCredentials(t *testing.T) {
	for _, dsn := range []string{"postgres://u:TOP_SECRET@127.0.0.1:1/test?sslmode=disable&connect_timeout=1", "password='TOP_SECRET"} {
		_, err := dbmigrate.Run(context.Background(), dsn)
		if err == nil || strings.Contains(err.Error(), "TOP_SECRET") {
			t.Fatalf("unsafe DSN error: %v", err)
		}
	}
}

func TestRunFreshAndIdempotent(t *testing.T) {
	dsn := testdb.New(t)
	ctx := context.Background()
	r, err := dbmigrate.Run(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if r.FromVersion != -1 || r.ToVersion != 2 || r.Applied != 2 || r.Dirty {
		t.Fatalf("unexpected result %+v", r)
	}
	db := open(t, dsn)
	execute(t, db, `INSERT INTO groups(id,name,created_at) VALUES('g','existing',123)`)
	r, err = dbmigrate.Run(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if r.FromVersion != 2 || r.ToVersion != 2 || r.Applied != 0 || r.Dirty {
		t.Fatalf("unexpected repeat %+v", r)
	}
	var count int
	if err = db.QueryRow("SELECT count(*) FROM groups WHERE id='g'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("business data changed: %d %v", count, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM pg_tables WHERE schemaname='public'").Scan(&count); err != nil || count != 19 {
		t.Fatalf("table coverage %d %v", count, err)
	}
	execute(t, db, `INSERT INTO users(id,username,display_name,password_hash,role,created_at,updated_at) VALUES('u','u','u','h','user',123,123); INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES('s','u',123,456); DELETE FROM users WHERE id='u'`)
	if err = db.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil || count != 0 {
		t.Fatalf("FK cascade failed: %d %v", count, err)
	}
	if _, err = db.Exec(`INSERT INTO agents(id,name,status,created_at,updated_at,disabled) VALUES('a','a','x',1,1,2)`); err == nil {
		t.Fatal("integer boolean constraint missing")
	}
}
func TestRefuseManagedCorruption(t *testing.T) {
	cases := []struct{ name, sql, want string }{
		{"dirty", "UPDATE arkade_migrate_versions SET dirty=true", "dirty"},
		{"future", "UPDATE arkade_migrate_versions SET version=99", "unsupported"},
		{"checksum", "UPDATE arkade_migrate_checksums SET checksum=repeat('0',64)", "checksum mismatch"},
		{"missing checksum", "DELETE FROM arkade_migrate_checksums", "missing"},
		{"column drift", "ALTER TABLE users ADD COLUMN surprise text", "schema drift"},
		{"index drift", "DROP INDEX idx_sessions_user", "schema drift"},
		{"constraint drift", "ALTER TABLE sessions DROP CONSTRAINT sessions_user_id_fkey", "schema drift"},
		{"monitor index drift", "DROP INDEX idx_monitor_incidents_history", "schema drift"},
		{"monitor column drift", "ALTER TABLE monitor_config DROP COLUMN secrets_ciphertext", "schema drift"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn := testdb.New(t)
			if _, err := dbmigrate.Run(context.Background(), dsn); err != nil {
				t.Fatal(err)
			}
			db := open(t, dsn)
			execute(t, db, tc.sql)
			_, err := dbmigrate.Run(context.Background(), dsn)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s got %v", tc.want, err)
			}
		})
	}
}
func TestRejectUnversionedDatabaseWithoutWriting(t *testing.T) {
	dsn := testdb.New(t)
	db := open(t, dsn)
	execute(t, db, "CREATE TABLE users(id text)")
	_, err := dbmigrate.Run(context.Background(), dsn)
	if err == nil || !strings.Contains(err.Error(), "unversioned") {
		t.Fatal(err)
	}
	var exists bool
	if err = db.QueryRow("SELECT to_regclass('public.arkade_migrate_versions') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("wrote metadata on rejection: %v %v", exists, err)
	}
}
func TestConcurrentRun(t *testing.T) {
	dsn := testdb.New(t)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := dbmigrate.Run(context.Background(), dsn); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}
func TestLockCancellation(t *testing.T) {
	dsn := testdb.New(t)
	db := open(t, dsn)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), "SELECT pg_advisory_lock(1095912257,1)"); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), "SELECT pg_advisory_unlock(1095912257,1)")
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = dbmigrate.Run(ctx, dsn)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("lock did not respect context: %v", err)
	}
}
