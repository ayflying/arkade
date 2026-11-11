package dbbackup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotIncludesCommittedWALAndRestores(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "live.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE business(id INTEGER PRIMARY KEY, content TEXT); INSERT INTO business VALUES(1, 'committed WAL data')`); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(source + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("test requires uncheckpointed WAL: %v", err)
	}
	backup := filepath.Join(root, "snapshot.db")
	if err := Snapshot(ctx, source, backup); err != nil {
		t.Fatal(err)
	}
	// A restored database is a separate SQLite file, not the live main file/WAL pair.
	restored := filepath.Join(root, "restored.db")
	if err := Snapshot(ctx, backup, restored); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE business SET content='changed after snapshot'`); err != nil {
		t.Fatal(err)
	}
	restoredDB, err := sql.Open("sqlite", restored)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredDB.Close()
	var got string
	if err := restoredDB.QueryRow(`SELECT content FROM business WHERE id=1`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "committed WAL data" {
		t.Fatalf("restored data = %q", got)
	}
	if err := Check(ctx, restored); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotCanceledRemovesOnlyReservedDestination(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination := filepath.Join(root, "canceled.db")
	if err := Snapshot(ctx, source, destination); err == nil {
		t.Fatal("canceled snapshot unexpectedly succeeded")
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("failed snapshot left a partial destination: %v", err)
	}
	if err := Check(context.Background(), source); err != nil {
		t.Fatalf("failed snapshot damaged source: %v", err)
	}
}

func TestSnapshotNeverOverwritesAndMissingSourceIsNotCreated(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	missing := filepath.Join(root, "missing.db")
	destination := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(destination, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Snapshot(ctx, missing, destination); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing source was created: %v", err)
	}
	source := filepath.Join(root, "source.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE t(id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Snapshot(ctx, source, destination); err == nil {
		t.Fatal("existing destination overwritten")
	}
	data, err := os.ReadFile(destination)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing destination changed: %q %v", data, err)
	}
}
