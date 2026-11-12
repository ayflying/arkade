package dbmigrate

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"arkade/hub/internal/testdb"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func TestForwardOnlyGuard(t *testing.T) {
	dsn := testdb.New(t)
	if _, err := Run(context.Background(), dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	driver, err := postgres.WithConnection(context.Background(), conn, &postgres.Config{SchemaName: "public", MigrationsTable: MigrationsTable})
	if err != nil {
		t.Fatal(err)
	}
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	m, err := migrate.NewWithInstance("iofs", &forwardSource{Driver: src}, "postgres", driver)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Down(); err == nil || !strings.Contains(err.Error(), "forward-only") {
		t.Fatalf("down not rejected: %v", err)
	}
	var version int
	var dirty bool
	if err = conn.QueryRowContext(context.Background(), "SELECT version,dirty FROM "+MigrationsTable).Scan(&version, &dirty); err != nil || version != 2 || dirty {
		t.Fatalf("down modified cursor: %d %v %v", version, dirty, err)
	}
}
func TestFailedMigrationLeavesDirtyAndRollsBackDDL(t *testing.T) {
	dsn := testdb.New(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	driver, err := postgres.WithConnection(context.Background(), conn, &postgres.Config{SchemaName: "public", MigrationsTable: MigrationsTable})
	if err != nil {
		t.Fatal(err)
	}
	if err = driver.SetVersion(1, true); err != nil {
		t.Fatal(err)
	}
	files, err := scripts()
	if err != nil {
		t.Fatal(err)
	}
	// A cancelled caller prevents any DDL/checksum transaction from committing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	guarded := &contextDriver{Postgres: driver, ctx: ctx, conn: conn, files: files}
	if err = guarded.Run(strings.NewReader(files[0].body)); err == nil {
		t.Fatal("cancelled migration succeeded")
	}
	// 取消时 database/sql 允许丢弃连接，状态必须用另一条真实连接核查。
	_ = conn.Close()
	conn, err = db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var version int
	var dirty bool
	err = conn.QueryRowContext(context.Background(), "SELECT version,dirty FROM "+MigrationsTable).Scan(&version, &dirty)
	if err != nil || version != 1 || !dirty {
		t.Fatalf("dirty not retained: %d %v %v", version, dirty, err)
	}
	var exists bool
	if err = conn.QueryRowContext(context.Background(), "SELECT to_regclass('public.users') IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Fatalf("partial business DDL: %v %v", exists, err)
	}
}
