package store

import (
	"context"
	"errors"
	"testing"
)

func TestExternalDraftIsolationAndCAS(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	a, err := db.SaveExternalComposeDraft(ctx, "a", "demo", "build: .", "hash", 0)
	if err != nil || a.Version != 1 {
		t.Fatal(a, err)
	}
	if _, err := db.SaveExternalComposeDraft(ctx, "a", "demo", "lost", "hash", 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("insert CAS %v", err)
	}
	if _, err := db.SaveExternalComposeDraft(ctx, "a", "demo", "lost", "hash", 9); !errors.Is(err, ErrConflict) {
		t.Fatalf("update CAS %v", err)
	}
	if _, err := db.GetExternalComposeDraft(ctx, "b", "demo"); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-agent leak", err)
	}
	if _, err := db.GetComposeStack(ctx, "a", "demo"); !errors.Is(err, ErrNotFound) {
		t.Fatal("draft registered stack", err)
	}
	b, err := db.SaveExternalComposeDraft(ctx, "a", "demo", "new", "hash", 1)
	if err != nil || b.Version != 2 {
		t.Fatal(b, err)
	}
	got, err := db.GetExternalComposeDraft(ctx, "a", "demo")
	if err != nil || got.Content != "new" || got.Version != 2 {
		t.Fatal(got, err)
	}
}
