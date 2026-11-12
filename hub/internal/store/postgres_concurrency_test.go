package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"arkade/hub/internal/testdb"
	"arkade/hub/internal/user"
	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"
	"github.com/lib/pq"
)

func openSharedTestDBs(t *testing.T) (*DB, *DB) {
	t.Helper()
	dsn := testdb.New(t)
	a, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	b, err := Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return a, b
}

func TestPostgresSQLStateNormalization(t *testing.T) {
	for _, code := range []string{"23505", "23503", "23502", "23514", "40001", "40P01"} {
		original := &pq.Error{Code: pq.ErrorCode(code)}
		err := normalizeExecError(gerror.Wrap(original, "wrapped"), "write")
		if code == "23505" {
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("unique state: %v", err)
			}
		} else {
			if errors.Is(err, ErrConflict) || !errors.Is(err, original) {
				t.Fatalf("SQLSTATE %s cause must be preserved: %v", code, err)
			}
		}
	}
	original := &pq.Error{Code: "08006"}
	if err := normalizeExecError(original, "write"); !errors.Is(err, original) {
		t.Fatal("unknown state lost original cause")
	}
}

func TestPostgresConcurrentPreprovisionAcrossPools(t *testing.T) {
	a, b := openSharedTestDBs(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, db := range []*DB{a, b} {
		go func(i int, db *DB) {
			<-start
			rec := newPreprovRecord("  Same   Host ")
			if i == 1 {
				rec.Name = "same host"
			}
			results <- db.CreatePreprovisionedAgent(context.Background(), rec, hashOf(rec.ID), time.Now())
		}(i, db)
	}
	close(start)
	success, conflict := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			success++
		} else if errors.Is(err, ErrAgentNameTaken) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestPostgresConcurrentLastAdminAcrossPools(t *testing.T) {
	a, b := openSharedTestDBs(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := a.CreateUser(ctx, mkUser(id, id, user.RoleAdmin)); err != nil {
			t.Fatal(err)
		}
	}
	results := make(chan error, 2)
	start := make(chan struct{})
	for i, db := range []*DB{a, b} {
		id := []string{"a", "b"}[i]
		go func(db *DB, id string) { <-start; results <- db.DeleteUser(ctx, id) }(db, id)
	}
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			success++
		} else if !errors.Is(err, ErrLastLocalAdmin) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("deleted %d admins", success)
	}
}

func TestPostgresConcurrentIdempotentMembership(t *testing.T) {
	a, b := openSharedTestDBs(t)
	ctx := context.Background()
	if err := a.CreateUser(ctx, mkUser("u", "u", user.RoleUser)); err != nil {
		t.Fatal(err)
	}
	if err := a.CreateGroup(ctx, user.Group{ID: "g", Name: "g"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(db *DB) {
			defer wg.Done()
			if err := db.AddUserToGroup(ctx, "u", "g"); err != nil {
				t.Error(err)
			}
		}([]*DB{a, b}[i%2])
	}
	wg.Wait()
	members, err := a.ListGroupMembers(ctx, "g")
	if err != nil || len(members) != 1 {
		t.Fatalf("members=%v err=%v", members, err)
	}
}

func TestPostgresTransactionLockCancellation(t *testing.T) {
	a, b := openSharedTestDBs(t)
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- a.TxModel(context.Background(), func(context.Context, gdb.TX) error { close(held); <-release; return nil })
	}()
	<-held
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := b.TxModel(ctx, func(context.Context, gdb.TX) error { t.Error("callback ran without lock"); return nil })
	close(release)
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if err == nil {
		t.Fatal("lock wait ignored context cancellation")
	}
	if err := b.TxModel(context.Background(), func(context.Context, gdb.TX) error { return nil }); err != nil {
		t.Fatalf("lock leaked: %v", err)
	}
}
