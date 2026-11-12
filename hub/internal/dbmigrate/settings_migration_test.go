package dbmigrate

import (
	"context"
	"path/filepath"
	"testing"
)

func latestTestVersion(t *testing.T) int {
	t.Helper()
	versions, err := embeddedVersions()
	if err != nil || len(versions) == 0 {
		t.Fatalf("embedded versions: %v", err)
	}
	return versions[len(versions)-1]
}
func TestSettingsMigrationPreservesLocalCredentials(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v10.db")
	db := openTestDB(t, path)
	applyLegacyUpTo(t, db, 10)
	if _, err := db.Exec(`INSERT INTO users VALUES('local','local','Local','$kept$hash','admin',0,123,456)`); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), path)
	if err != nil || result.ToVersion != 11 || result.Applied != 1 {
		t.Fatal(result, err)
	}
	var hash string
	var forced int
	if err := db.QueryRow(`SELECT password_hash,must_change_password FROM users WHERE id='local'`).Scan(&hash, &forced); err != nil || hash != "$kept$hash" || forced != 0 {
		t.Fatal("existing credential changed", err)
	}
	var enabled, version int
	var site string
	if err := db.QueryRow(`SELECT site_name,registration_enabled,version FROM system_settings WHERE id=1`).Scan(&site, &enabled, &version); err != nil || site != "Arkade" || enabled != 0 || version != 1 {
		t.Fatal("unsafe seed", err)
	}
	if _, err := db.Exec(`INSERT INTO system_settings SELECT 2,site_name,login_notice,registration_enabled,registration_group_id,session_ttl_seconds,password_min_length,login_max_attempts,login_window_seconds,audit_retention_days,version FROM system_settings`); err == nil {
		t.Fatal("singleton id not enforced")
	}
}
