package dbmigrate

import (
	"arkade/hub/internal/testdb"
	"context"
	"database/sql"
	"testing"
)

func TestMonitoringUpgradeFromV1(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	files, err := scripts()
	if err != nil {
		t.Fatal(err)
	}
	if files[0].version != 1 {
		t.Fatal("expected immutable v1 migration")
	}
	if _, err = db.ExecContext(ctx, files[0].body); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `CREATE TABLE arkade_migrate_versions(version bigint NOT NULL PRIMARY KEY,dirty boolean NOT NULL); INSERT INTO arkade_migrate_versions VALUES(1,false)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO arkade_migrate_checksums(version,checksum) VALUES(1,$1)`, files[0].checksum); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO groups(id,name,created_at) VALUES('keep','v1-data',123)`); err != nil {
		t.Fatal(err)
	}
	r, err := Run(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if r.FromVersion != 1 || r.ToVersion != 2 || r.Applied != 1 || r.Dirty {
		t.Fatalf("upgrade: %+v", r)
	}
	var count int
	if err = db.QueryRowContext(ctx, `SELECT count(*) FROM groups WHERE id='keep'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("lost v1 data %d %v", count, err)
	}
	var config, secrets string
	var version int64
	if err = db.QueryRowContext(ctx, `SELECT config_json,secrets_ciphertext,version FROM monitor_config WHERE id=1`).Scan(&config, &secrets, &version); err != nil || config != "{}" || secrets != "" || version != 1 {
		t.Fatalf("config seed %q %q %d %v", config, secrets, version, err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO monitor_config(id) VALUES(2)`); err == nil {
		t.Fatal("singleton constraint missing")
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO monitor_incidents(id,fingerprint,agent_id) VALUES('i','f','deleted-agent'); INSERT INTO monitor_deliveries(id,incident_id) VALUES('d','deleted-incident')`); err != nil {
		t.Fatalf("monitor history must not require live agent/incident FK: %v", err)
	}
	if _, err = Run(ctx, dsn); err != nil {
		t.Fatalf("v2 verification: %v", err)
	}
}
