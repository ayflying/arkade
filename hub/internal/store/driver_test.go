package store

import (
	"arkade/hub/internal/testdb"
	"context"
	"strings"
	"testing"
)

func TestPostgresPoolSupportsIndependentConnections(t *testing.T) {
	inst, err := openGDB(testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	defer closeGDB(inst)
	raw, err := masterOf(inst)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Stats().MaxOpenConnections != 20 {
		t.Fatalf("max open = %d", raw.Stats().MaxOpenConnections)
	}
	ctx := context.Background()
	a, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := raw.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var pa, pb int
	if err = a.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pa); err != nil {
		t.Fatal(err)
	}
	if err = b.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pb); err != nil {
		t.Fatal(err)
	}
	if pa == pb {
		t.Fatal("connections share one PG backend")
	}
}

func TestDSNParsing(t *testing.T) {
	for _, dsn := range []string{`host=localhost user=arkade password='s p\\a\'ss' dbname=arkade sslmode=disable`, `postgres://arkade:s%20p%5Ca%27ss@localhost/arkade?sslmode=disable`} {
		values, err := parseDSN(dsn)
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("password") != "s p\\a'ss" {
			t.Fatalf("password parsing: %q", values.Get("password"))
		}
	}
	for _, dsn := range []string{"", "local.db", ":memory:", `host=x password='secret`} {
		_, err := openGDB(dsn)
		if err == nil {
			t.Fatalf("accepted invalid DSN %q", dsn)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatal("DSN secret leaked")
		}
	}
}
