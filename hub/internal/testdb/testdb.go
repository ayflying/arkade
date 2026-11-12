// Package testdb provisions isolated real PostgreSQL databases for tests.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// New creates one database per call. The base role must have CREATEDB privilege.
// Tests must close their own pools before cleanup (register after New returns).
// A missing or unreachable server is a hard failure, never a skipped PG test.
func New(t testing.TB) string {
	t.Helper()
	base := os.Getenv("ARKADE_TEST_PG_DSN")
	if base == "" {
		t.Fatal("ARKADE_TEST_PG_DSN is required for real PostgreSQL tests (role needs CREATEDB)")
	}
	u, err := url.Parse(base)
	isURL := err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql")
	if !isURL && !strings.Contains(base, "dbname=") {
		t.Fatal("ARKADE_TEST_PG_DSN must be a PostgreSQL URL or libpq keyword DSN with dbname")
	}
	admin, err := sql.Open("postgres", base)
	if err != nil {
		t.Fatal("invalid ARKADE_TEST_PG_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err = admin.PingContext(ctx); err != nil {
		admin.Close()
		t.Fatal("PostgreSQL test server unavailable (check ARKADE_TEST_PG_DSN and server status)")
	}
	var random [12]byte
	if _, err = rand.Read(random[:]); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	name := "arkade_test_" + hex.EncodeToString(random[:])
	if _, err = admin.ExecContext(ctx, `CREATE DATABASE "`+name+`" TEMPLATE template0`); err != nil {
		admin.Close()
		t.Fatalf("create isolated PostgreSQL database (CREATEDB required): %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// FORCE also releases forgotten client connections; never touches base DB.
		if _, err := admin.ExecContext(ctx, `DROP DATABASE "`+name+`" WITH (FORCE)`); err != nil {
			t.Errorf("drop isolated PostgreSQL database %s: %v", name, err)
		}
	})
	if isURL {
		u.Path = "/" + name
		u.RawPath = ""
		return u.String()
	}
	return base + " dbname=" + name
}
