package store

import (
	"context"

	"errors"
	"github.com/gogf/gf/v2/database/gdb"
	"strings"
	"testing"
	"time"
)

func operationRequest() ComposeOperationRequest {
	return ComposeOperationRequest{AgentID: "a", RequestID: "request", Name: "web", Action: "up", Fingerprint: "digest", ExpectedVersion: 1}
}
func prepareTestOperation(t *testing.T, db *DB, req ComposeOperationRequest) (ComposeOperation, ComposeStack) {
	t.Helper()
	op, s, dup, err := db.PrepareComposeOperation(context.Background(), req)
	if err != nil || dup {
		t.Fatalf("prepare: op=%+v duplicate=%v error=%v", op, dup, err)
	}
	return op, s
}
func strptr(s string) *string { return &s }

func TestComposeOperationDuplicateNeverResaves(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateComposeStack(ctx, mkStack("a", "web", "old")); err != nil {
		t.Fatal(err)
	}
	req := operationRequest()
	req.SaveContent = strptr("new")
	req.SaveEnv = strptr("SECRET=x")
	req.ConfigHash = "newhash"
	op, s := prepareTestOperation(t, db, req)
	if s.Version != 2 || s.EnvContent != "SECRET=x" {
		t.Fatalf("bad save: %+v", s)
	}
	again, s, dup, err := db.PrepareComposeOperation(ctx, req)
	if err != nil || !dup || s.Version != 2 || again.StackVersion != op.StackVersion {
		t.Fatalf("retry: %+v %+v %v %v", again, s, dup, err)
	}
	for _, change := range []func(*ComposeOperationRequest){func(r *ComposeOperationRequest) { r.Fingerprint = "different" }, func(r *ComposeOperationRequest) { r.Name = "other" }, func(r *ComposeOperationRequest) { r.Action = "remove" }} {
		other := req
		change(&other)
		if _, _, _, err = db.PrepareComposeOperation(ctx, other); !errors.Is(err, ErrConflict) {
			t.Fatalf("key rebinding: %v", err)
		}
	}
	current, _ := db.GetComposeStack(ctx, "a", "web")
	if current.Version != 2 {
		t.Fatal("retry resaved")
	}
}

func TestComposeOperationVersionGuardsAndKnownTask(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	_ = db.CreateComposeStack(ctx, mkStack("a", "web", "old"))
	req := operationRequest()
	stale := req
	stale.ExpectedVersion = 2
	if _, _, _, err := db.PrepareComposeOperation(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	stale = req
	stale.ExpectedHash = "incorrect"
	if _, _, _, err := db.PrepareComposeOperation(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale hash: %v", err)
	}
	op, _ := prepareTestOperation(t, db, req)
	_, _, _ = db.UpdateComposeStack(ctx, "a", "web", 1, "new", "newhash")
	if _, s, dup, err := db.PrepareComposeOperation(ctx, req); !errors.Is(err, ErrConflict) || !dup || s.ID != "" {
		t.Fatalf("retry read newer config: %+v %v %v", s, dup, err)
	}
	if err := db.AttachOperationTask(ctx, "a", req.RequestID, "task"); err != nil {
		t.Fatal(err)
	}
	if err := db.AttachOperationTask(ctx, "a", req.RequestID, "task"); err != nil {
		t.Fatal(err)
	}
	if err := db.AttachOperationTask(ctx, "a", req.RequestID, "other"); !errors.Is(err, ErrConflict) {
		t.Fatalf("rebind task: %v", err)
	}
	_ = db.DeleteComposeStack(ctx, "a", "web")
	got, s, dup, err := db.PrepareComposeOperation(ctx, req)
	if err != nil || !dup || got.TaskID != "task" || s.ID != "" {
		t.Fatalf("known deleted task: %+v %+v %v %v", got, s, dup, err)
	}
	found, err := db.FindOperationByTask(ctx, "a", "web", "task")
	if err != nil || found.StackID != op.StackID {
		t.Fatalf("find: %+v %v", found, err)
	}
	if _, err = db.FindOperationByTask(ctx, "a", "wrong", "task"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestComposeOperationCreateAtomicRollback(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	req := operationRequest()
	req.Create = true
	req.StackID = "new"
	req.SaveContent = strptr("secret compose")
	req.ConfigHash = "digest"
	// Inject an operation insert failure after the stack insert. Both must roll back.
	_, err := rawDBOf(t, db).ExecContext(ctx, `CREATE FUNCTION reject_operation_fn() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$; CREATE TRIGGER reject_operation BEFORE INSERT ON compose_operation_requests FOR EACH ROW EXECUTE FUNCTION reject_operation_fn()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = db.PrepareComposeOperation(ctx, req); err == nil {
		t.Fatal("expected insert failure")
	}
	if _, err = db.GetComposeStack(ctx, "a", "web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("orphan stack: %v", err)
	}
	_, err = rawDBOf(t, db).ExecContext(ctx, `DROP TRIGGER reject_operation ON compose_operation_requests; DROP FUNCTION reject_operation_fn()`)
	if err != nil {
		t.Fatal(err)
	}
	op, s := prepareTestOperation(t, db, req)
	if s.Version != 1 || op.StackID != "new" {
		t.Fatalf("create: %+v %+v", op, s)
	}
	if _, _, dup, err := db.PrepareComposeOperation(ctx, req); err != nil || !dup {
		t.Fatalf("duplicate create: %v %v", dup, err)
	}
}

func TestComposeOperationFinalizeConditions(t *testing.T) {
	for _, action := range []string{"up", "remove"} {
		for _, mutation := range []string{"none", "id", "version", "hash", "failure"} {
			t.Run(action+"/"+mutation, func(t *testing.T) {
				db := newTestDB(t)
				ctx := context.Background()
				_ = db.CreateComposeStack(ctx, mkStack("a", "web", "old"))
				req := operationRequest()
				req.Action = action
				op, _ := prepareTestOperation(t, db, req)
				switch mutation {
				case "id":
					_, _ = rawDBOf(t, db).ExecContext(ctx, `UPDATE compose_stacks SET id = 'replacement' WHERE agent_id = 'a'`)
				case "version":
					_, _ = rawDBOf(t, db).ExecContext(ctx, `UPDATE compose_stacks SET version = 2 WHERE agent_id = 'a'`)
				case "hash":
					_, _ = rawDBOf(t, db).ExecContext(ctx, `UPDATE compose_stacks SET config_hash = 'other' WHERE agent_id = 'a'`)
				}
				final, err := db.FinalizeComposeOperation(ctx, op, mutation != "failure")
				if err != nil || !final.Finalized {
					t.Fatalf("finalize: %+v %v", final, err)
				}
				s, err := db.GetComposeStack(ctx, "a", "web")
				if mutation == "none" && action == "remove" {
					if !errors.Is(err, ErrNotFound) || !final.RecordRemoved {
						t.Fatalf("not removed: %+v %v", final, err)
					}
					replacement := mkStack("a", "web", "replacement")
					replacement.ID = "replacement"
					if err = db.CreateComposeStack(ctx, replacement); err != nil {
						t.Fatal(err)
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					synced := mutation == "none" && action == "up"
					if s.SyncedAt.IsZero() == synced || final.RecordRemoved {
						t.Fatalf("invalid effect: %+v %+v", s, final)
					}
				}
				again, err := db.FinalizeComposeOperation(ctx, op, true)
				if err != nil || again != final {
					t.Fatalf("repeat: %+v %v", again, err)
				}
				if _, err = db.GetComposeStack(ctx, "a", "web"); err != nil {
					t.Fatalf("repeat deleted replacement: %v", err)
				}
			})
		}
	}
}

func TestComposeOperationRetentionAndCap(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	_ = db.CreateComposeStack(ctx, mkStack("a", "web", "old"))
	req := operationRequest()
	op, _ := prepareTestOperation(t, db, req)
	old := time.Now().Add(-25 * time.Hour).Unix()
	_, _ = rawDBOf(t, db).ExecContext(ctx, `UPDATE compose_operation_requests SET created_at = $1`, old)
	req.RequestID = "second"
	prepareTestOperation(t, db, req)
	var count int
	_ = rawDBOf(t, db).QueryRowContext(ctx, `SELECT COUNT(*) FROM compose_operation_requests`).Scan(&count)
	if count != 2 {
		t.Fatal("pruned nonfinalized operation")
	}
	if _, err := db.FinalizeComposeOperation(ctx, op, false); err != nil {
		t.Fatal(err)
	}
	req.RequestID = "third"
	prepareTestOperation(t, db, req)
	_ = rawDBOf(t, db).QueryRowContext(ctx, `SELECT COUNT(*) FROM compose_operation_requests`).Scan(&count)
	if count != 2 {
		t.Fatalf("finalized TTL not pruned: %d", count)
	}
	err := db.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		for i := count; i < maxComposeOperations; i++ {
			_, err := tx.Exec(`INSERT INTO compose_operation_requests (`+strings.Join(composeOperationFields(), ", ")+`) VALUES ('a', ?, 'web', 'up', 'fingerprint', '', 'stack', 1, 'hash', ?, 0, '', 0)`, "cap-"+time.Unix(int64(i), 0).Format(time.RFC3339), old)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	req.RequestID = "over-cap"
	req.SaveContent = strptr("must not save")
	req.ConfigHash = "other"
	if _, _, _, err = db.PrepareComposeOperation(ctx, req); !errors.Is(err, ErrConflict) {
		t.Fatalf("cap: %v", err)
	}
	s, _ := db.GetComposeStack(ctx, "a", "web")
	if s.Version != 1 {
		t.Fatal("cap saved configuration")
	}
}
