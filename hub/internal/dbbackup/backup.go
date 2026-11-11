// Package dbbackup creates consistent SQLite snapshots, including committed WAL data.
package dbbackup

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"

	_ "github.com/gogf/gf/contrib/drivers/sqlite/v2"
	"github.com/gogf/gf/v2/errors/gerror"
)

// Snapshot copies a live database using SQLite VACUUM INTO, never a main-file copy.
// The destination must not exist. No migration is run on either database.
func Snapshot(ctx context.Context, source, destination string) (err error) {
	source, err = filepath.Abs(source)
	if err != nil {
		return gerror.Wrap(err, "resolve source database")
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return gerror.Wrap(err, "resolve snapshot destination")
	}
	info, err := os.Stat(source)
	if err != nil {
		return gerror.Wrap(err, "inspect source database")
	}
	if !info.Mode().IsRegular() || source == destination {
		return gerror.New("source must be a regular database and destination must differ")
	}
	// Reserve the exact destination; never overwrite a backup or existing data.
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return gerror.Wrap(err, "reserve snapshot destination")
	}
	if err = file.Close(); err != nil {
		return gerror.Wrap(err, "close reserved snapshot")
	}
	defer func() {
		if err != nil {
			// Only this invocation's exclusively created destination is removed.
			_ = os.Remove(destination)
		}
	}()
	db, err := sql.Open("sqlite", source)
	if err != nil {
		return gerror.Wrap(err, "open snapshot source")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return gerror.Wrap(err, "configure snapshot connection")
	}
	if _, err = db.ExecContext(ctx, `VACUUM INTO ?`, destination); err != nil {
		return gerror.Wrap(err, "create consistent SQLite snapshot")
	}
	return Check(ctx, destination)
}

// Check runs SQLite integrity_check without applying schema migrations.
func Check(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return gerror.Wrap(err, "inspect database")
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return gerror.New("database must be a nonempty regular file")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return gerror.Wrap(err, "open database for integrity check")
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check`)
	if err != nil {
		return gerror.Wrap(err, "check SQLite integrity")
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return gerror.Wrap(err, "read integrity result")
		}
		if result != "ok" {
			return gerror.Newf("SQLite integrity check failed: %s", result)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return gerror.Wrap(err, "iterate integrity results")
	}
	if count != 1 {
		return gerror.New("SQLite integrity check did not return a single ok result")
	}
	return nil
}
