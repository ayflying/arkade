package dbmigrate

import (
	"context"
	"database/sql"
	"testing"

	"arkade/hub/internal/testdb"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

func TestSessionTTLUpgradePreservesCustomSettingsAndExistingSessions(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		ttl, version         int64
		wantTTL, wantVersion int64
	}{
		{"untouched 24h default", 86400, 1, 2592000, 2},
		{"explicitly saved 24h", 86400, 2, 86400, 2},
		{"custom 72h matches old config fallback", 259200, 2, 259200, 2},
		{"custom value without version change", 604800, 1, 604800, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dsn := testdb.New(t)
			db, err := sql.Open("postgres", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			migrateBeforeSessionTTL(t, ctx, db)
			// The untouched case intentionally uses the real v8 seed as-is.
			if tc.ttl != 86400 || tc.version != 1 {
				if _, err = db.ExecContext(ctx, `UPDATE system_settings SET session_ttl_seconds=$1, version=$2 WHERE id=1`, tc.ttl, tc.version); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = db.ExecContext(ctx, `
				INSERT INTO users(id,username,display_name,password_hash,role,created_at,updated_at)
				VALUES('u','u','u','h','user',123,123);
				INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES('old','u',123,86523)`); err != nil {
				t.Fatal(err)
			}
			r, err := Run(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			if r.FromVersion != 8 || r.ToVersion != latestVersion(t) || r.Dirty {
				t.Fatalf("unexpected upgrade: %+v", r)
			}
			var ttl, version, expiresAt int64
			if err = db.QueryRowContext(ctx, `SELECT session_ttl_seconds,version FROM system_settings WHERE id=1`).Scan(&ttl, &version); err != nil {
				t.Fatal(err)
			}
			if ttl != tc.wantTTL || version != tc.wantVersion {
				t.Fatalf("settings = (%d, v%d), want (%d, v%d)", ttl, version, tc.wantTTL, tc.wantVersion)
			}
			if err = db.QueryRowContext(ctx, `SELECT expires_at FROM sessions WHERE token_hash='old'`).Scan(&expiresAt); err != nil || expiresAt != 86523 {
				t.Fatalf("existing session expiry changed: %d, err=%v", expiresAt, err)
			}
		})
	}
}

func migrateBeforeSessionTTL(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	driver, err := postgres.WithConnection(ctx, conn, &postgres.Config{SchemaName: "public", MigrationsTable: MigrationsTable})
	if err != nil {
		t.Fatal(err)
	}
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	files, err := scripts()
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.NewWithInstance("iofs", &forwardSource{Driver: src}, "postgres", &contextDriver{Postgres: driver, ctx: ctx, conn: conn, files: files})
	if err != nil {
		t.Fatal(err)
	}
	if err = m.Migrate(8); err != nil {
		t.Fatal(err)
	}
}
