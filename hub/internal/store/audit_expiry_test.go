package store

import (
	"arkade/hub/internal/audit"
	"context"
	"testing"
	"time"
)

func TestPruneAuditBeforeBoundary(t *testing.T) {
	db := newTestDB(t)
	cutoff := time.Date(2026, 11, 12, 3, 0, 0, 0, time.UTC)
	for _, row := range []struct {
		id string
		at time.Time
	}{{"older", cutoff.Add(-time.Second)}, {"boundary", cutoff}, {"newer", cutoff.Add(time.Second)}} {
		if err := db.WriteAudit(audit.Record{ID: row.id, CreatedAt: row.at, Action: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := db.PruneAuditBefore(context.Background(), cutoff)
	if err != nil || n != 1 {
		t.Fatalf("deleted=%d error=%v", n, err)
	}
	var count int
	if err := rawDBOf(t, db).QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE id IN ('boundary','newer')`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("boundary count=%d err=%v", count, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := db.PruneAuditBefore(ctx, cutoff.Add(time.Hour)); err == nil {
		t.Fatal("canceled prune succeeded")
	}
}
