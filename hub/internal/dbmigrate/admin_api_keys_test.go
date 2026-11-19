package dbmigrate

import (
	"context"
	"database/sql"
	"testing"

	"arkade/hub/internal/testdb"
)

func TestAPIKeysUpgradeFromV5PreservesExistingKey(t *testing.T) {
	ctx := t.Context()
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
	for _, f := range files {
		if f.version > 5 {
			break
		}
		if _, err := db.ExecContext(ctx, f.body); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO arkade_migrate_checksums(version,checksum) VALUES($1,$2)`, f.version, f.checksum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE arkade_migrate_versions(version bigint NOT NULL PRIMARY KEY,dirty boolean NOT NULL);
	 INSERT INTO arkade_migrate_versions VALUES(5,false);
	 INSERT INTO users(id,username,display_name,password_hash,role,created_at,updated_at) VALUES('keep','keep','管理员','h','admin',123,123);
	 INSERT INTO admin_api_keys(id,user_id,name,token_hash,created_at) VALUES(repeat('a',32),'keep','existing',repeat('b',64),123);`); err != nil {
		t.Fatal(err)
	}
	res, err := Run(ctx, dsn)
	if err != nil || res.FromVersion != 5 || res.ToVersion != 6 || res.Applied != 1 || res.Dirty {
		t.Fatalf("upgrade: %+v %v", res, err)
	}
	var preserved bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM admin_api_keys WHERE user_id='keep' AND token_hash=repeat('b',64))`).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("existing key not preserved: %t %v", preserved, err)
	}
}

func TestAdminAPIKeysUpgradeFromV4(t *testing.T) {
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
	for _, f := range files {
		if f.version >= 5 {
			break
		}
		if _, err = db.ExecContext(ctx, f.body); err != nil {
			t.Fatal(err)
		}
		if _, err = db.ExecContext(ctx, `INSERT INTO arkade_migrate_checksums(version,checksum) VALUES($1,$2)`, f.version, f.checksum); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE arkade_migrate_versions(version bigint NOT NULL PRIMARY KEY,dirty boolean NOT NULL);
	 INSERT INTO arkade_migrate_versions VALUES(4,false);
	 INSERT INTO users(id,username,display_name,password_hash,role,created_at,updated_at) VALUES('keep','keep','管理员','h','admin',123,123);
	 INSERT INTO sessions(token_hash,user_id,created_at,expires_at) VALUES('existing-session','keep',123,456);`); err != nil {
		t.Fatal(err)
	}
	res, err := Run(ctx, dsn)
	if err != nil || res.FromVersion != 4 || res.ToVersion != 6 || res.Applied != 2 || res.Dirty {
		t.Fatalf("upgrade: %+v %v", res, err)
	}
	var username string
	if err := db.QueryRowContext(ctx, `SELECT u.username FROM users u JOIN sessions s ON s.user_id = u.id WHERE s.token_hash = 'existing-session'`).Scan(&username); err != nil || username != "keep" {
		t.Fatalf("upgrade lost existing user or session: %q %v", username, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_api_keys(id,user_id,name,token_hash,created_at) VALUES('key','keep','key','plaintext',123)`); err == nil {
		t.Fatal("digest constraint accepted plaintext")
	}
}
