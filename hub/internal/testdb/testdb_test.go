package testdb

import (
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestKeywordDSNDatabaseOverride(t *testing.T) {
	base := os.Getenv("ARKADE_TEST_PG_DSN")
	if strings.HasPrefix(base, "postgres://") || strings.HasPrefix(base, "postgresql://") {
		u, err := url.Parse(base)
		if err != nil {
			t.Fatal("invalid test URL")
		}
		quote := func(s string) string { return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'" }
		fields := []string{"host=" + quote(u.Hostname()), "dbname=" + quote(strings.TrimPrefix(u.Path, "/"))}
		if u.Port() != "" {
			fields = append(fields, "port="+quote(u.Port()))
		}
		if u.User != nil {
			fields = append(fields, "user="+quote(u.User.Username()))
			if pass, ok := u.User.Password(); ok {
				fields = append(fields, "password="+quote(pass))
			}
		}
		for k, values := range u.Query() {
			fields = append(fields, k+"="+quote(values[0]))
		}
		base = strings.Join(fields, " ")
	}
	t.Setenv("ARKADE_TEST_PG_DSN", base)
	dsn := New(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal("invalid provisioned DSN")
	}
	t.Cleanup(func() { db.Close() })
	var name string
	if err = db.QueryRow("SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "arkade_test_") {
		t.Fatalf("keyword duplicate dbname did not select isolated database: %s", name)
	}
}

func TestNewCreatesIndependentDatabases(t *testing.T) {
	a, err := sql.Open("postgres", New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := sql.Open("postgres", New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	var an, bn string
	if err = a.QueryRow("SELECT current_database()").Scan(&an); err != nil {
		t.Fatal(err)
	}
	if err = b.QueryRow("SELECT current_database()").Scan(&bn); err != nil {
		t.Fatal(err)
	}
	if an == bn {
		t.Fatalf("databases not isolated: %s", an)
	}
	if _, err = a.Exec("CREATE TABLE isolation_probe(id integer)"); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = b.QueryRow("SELECT to_regclass('public.isolation_probe') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("database B can see database A's tables")
	}
}
