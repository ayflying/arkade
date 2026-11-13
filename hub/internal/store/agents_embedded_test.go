package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestEnsureEmbeddedLocalAgentIdempotent(t *testing.T) {
	db, other := openSharedTestDBs(t)
	ctx := context.Background()
	results := make(chan error, 4)
	for _, pool := range []*DB{db, other, db, other} {
		go func(pool *DB) { results <- pool.EnsureEmbeddedLocalAgent(ctx) }(pool)
	}
	for i := 0; i < 4; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	rec, err := db.GetAgent(ctx, LocalAgentID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Name != LocalAgentName || rec.DisplayName != LocalAgentName || rec.Status != "active" || !rec.Preprovisioned || rec.Disabled {
		t.Fatalf("initial %+v", rec)
	}
	var count int
	if err := rawDBOf(t, db).QueryRow(`SELECT count(*) FROM agent_tokens WHERE agent_id=$1`, LocalAgentID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("must not create token %d %v", count, err)
	}
	before := rec
	if err := other.EnsureEmbeddedLocalAgent(ctx); err != nil {
		t.Fatal(err)
	}
	rec, _ = db.GetAgent(ctx, LocalAgentID)
	if !reflect.DeepEqual(before, rec) {
		t.Fatalf("restart mutated identity %+v -> %+v", before, rec)
	}
}

func TestEnsureEmbeddedLocalAgentRevokesLegacyPreservesMetadata(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	hash := hashOf("legacy-local-token")
	if err := db.EnsureLocalAgent(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDBOf(t, db).Exec(`UPDATE agents SET name='旧名字',display_name='自定义',note='备注',labels='{"role":"local"}',disabled=1,direct_address='localhost',updated_at=123 WHERE id=$1`, LocalAgentID); err != nil {
		t.Fatal(err)
	}
	if _, err := rawDBOf(t, db).Exec(`INSERT INTO groups(id,name,created_at) VALUES('local-group','local-group',1); INSERT INTO group_agents(group_id,agent_id,permission) VALUES('local-group','agent-local','read')`); err != nil {
		t.Fatal(err)
	}
	before, _ := db.GetAgent(ctx, LocalAgentID)
	if err := db.EnsureEmbeddedLocalAgent(ctx); err != nil {
		t.Fatal(err)
	}
	after, _ := db.GetAgent(ctx, LocalAgentID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("legacy metadata overwritten %+v -> %+v", before, after)
	}
	if _, err := db.LookupAgentByTokenHash(ctx, hash); !errors.Is(err, ErrAgentTokenNotFound) {
		t.Fatalf("legacy credential still usable: %v", err)
	}
	if err := db.UpdateEmbeddedLocalAgentRuntime(ctx, AgentRecord{ID: LocalAgentID, Name: "不应覆盖", Status: "online", Version: "test", Runtime: "docker", RuntimeVersion: "27", LastSeen: 456, UpdatedAt: 456, Labels: map[string]string{"bad": "overwrite"}}); err != nil {
		t.Fatal(err)
	}
	updated, _ := db.GetAgent(ctx, LocalAgentID)
	expected := before
	expected.Status = "online"
	expected.Version = "test"
	expected.Runtime = "docker"
	expected.RuntimeVersion = "27"
	expected.LastSeen = 456
	expected.UpdatedAt = 456
	if !reflect.DeepEqual(expected, updated) {
		t.Fatalf("runtime update changed metadata %+v -> %+v", expected, updated)
	}
	var count int
	if err := rawDBOf(t, db).QueryRow(`SELECT count(*) FROM group_agents WHERE agent_id=$1`, LocalAgentID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("group ownership changed %d %v", count, err)
	}
}

func TestEnsureEmbeddedLocalAgentRejectsTakeover(t *testing.T) {
	for _, fixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-name", true: "fixed-id"}[fixed], func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			rec := newPreprovRecord(LocalAgentName)
			if fixed {
				rec.ID = LocalAgentID
				rec.Name = "different"
				if err := db.UpsertAgent(ctx, rec); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := db.CreatePreprovisionedAgent(ctx, rec, hashOf("other"), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := db.ListAgents(ctx)
			if err := db.EnsureEmbeddedLocalAgent(ctx); err == nil {
				t.Fatal("takeover accepted")
			}
			after, _ := db.ListAgents(ctx)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("conflict changed records")
			}
		})
	}
}
