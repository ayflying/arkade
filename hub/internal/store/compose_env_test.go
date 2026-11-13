package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestComposeEnvCreateReadList(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"alpha", "beta"} {
		s := mkStack("a1", name, "services: {}\n")
		if name == "alpha" {
			s.EnvContent = "# exact bytes\r\nTOKEN='你好=world'\r\nEMPTY=\r\n"
		}
		if err := db.CreateComposeStack(ctx, s); err != nil {
			t.Fatal(err)
		}
		got, err := db.GetComposeStack(ctx, s.AgentID, s.Name)
		if err != nil {
			t.Fatal(err)
		}
		if got.EnvContent != s.EnvContent || got.Content != s.Content || got.ConfigHash != s.ConfigHash {
			t.Fatalf("create/read lost project content: %+v", got)
		}
	}
	list, err := db.ListComposeStacks(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "alpha" || list[0].EnvContent != "# exact bytes\r\nTOKEN='你好=world'\r\nEMPTY=\r\n" || list[1].EnvContent != "" {
		t.Fatalf("list lost env content/order: %+v", list)
	}
}

func TestUpdateComposeProjectOptimisticEnv(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	s := mkStack("a1", "web", "original")
	s.EnvContent = "OLD=1\n"
	s.SyncedAt = time.Unix(100, 0).UTC()
	if err := db.CreateComposeStack(ctx, s); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(200, 0).UTC()
	updated, ok, err := db.UpdateComposeProject(ctx, "a1", "web", "new compose", "NEW=2\n", "project-hash", 1, at)
	if err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	if updated.Content != "new compose" || updated.EnvContent != "NEW=2\n" || updated.ConfigHash != "project-hash" || updated.Version != 2 || !updated.UpdatedAt.Equal(at) || !updated.SyncedAt.Equal(s.SyncedAt) {
		t.Fatalf("incomplete project update: %+v", updated)
	}
	stale, ok, err := db.UpdateComposeProject(ctx, "a1", "web", "stale", "STALE=1", "stale-hash", 1, at.Add(time.Hour))
	if err != nil || ok || stale != (ComposeStack{}) {
		t.Fatalf("stale update: stack=%+v ok=%v err=%v", stale, ok, err)
	}
	got, err := db.GetComposeStack(ctx, "a1", "web")
	if err != nil || !reflect.DeepEqual(got, updated) {
		t.Fatalf("stale update changed row: %+v err=%v", got, err)
	}
	// Explicit empty env removes the previous file contents, without losing compose.
	cleared, ok, err := db.UpdateComposeProject(ctx, "a1", "web", updated.Content, "", "cleared-hash", 2, at.Add(time.Minute))
	if err != nil || !ok || cleared.EnvContent != "" || cleared.Version != 3 || cleared.Content != updated.Content {
		t.Fatalf("clear env: stack=%+v ok=%v err=%v", cleared, ok, err)
	}
	_, ok, err = db.UpdateComposeProject(ctx, "a1", "missing", "c", "e", "h", 1, at)
	if ok || !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing project: ok=%v err=%v", ok, err)
	}
}

// TestComposeEnvDefault verifies fresh v1 defaults without historical migration fixtures.
func TestComposeEnvDefault(t *testing.T) {
	db := newTestDB(t)
	_, err := rawDBOf(t, db).Exec(`INSERT INTO compose_stacks(id,agent_id,name,content,config_hash,created_at,updated_at) VALUES ('default','a','web','compose','hash',1,1)`)
	if err != nil {
		t.Fatal(err)
	}
	var env string
	if err = rawDBOf(t, db).QueryRow(`SELECT env_content FROM compose_stacks WHERE id='default'`).Scan(&env); err != nil {
		t.Fatal(err)
	}
	if env != "" {
		t.Fatalf("default env=%q", env)
	}
	if _, err = rawDBOf(t, db).Exec(`UPDATE compose_stacks SET env_content=NULL WHERE id='default'`); sqlState(err) != "23502" {
		t.Fatalf("env must be NOT NULL: %v", err)
	}
}

func TestUpdateComposeProjectPreservesProvidedEnv(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	s := mkStack("a1", "web", "old")
	s.EnvContent = "SECRET=keep\n"
	if err := db.CreateComposeStack(ctx, s); err != nil {
		t.Fatal(err)
	}
	updated, ok, err := db.UpdateComposeProject(ctx, "a1", "web", "new", s.EnvContent, "new-hash", 1, time.Now().UTC())
	if err != nil || !ok || updated.EnvContent != s.EnvContent || updated.Version != 2 {
		t.Fatalf("project update: %+v ok=%v err=%v", updated, ok, err)
	}
	_, ok, err = db.UpdateComposeProject(ctx, "a1", "web", "stale", "SECRET=stale\n", "stale-hash", 1, time.Now().UTC())
	if err != nil || ok {
		t.Fatalf("project stale update: ok=%v err=%v", ok, err)
	}
	got, err := db.GetComposeStack(ctx, "a1", "web")
	if err != nil || !reflect.DeepEqual(got, updated) {
		t.Fatalf("project stale update changed row: %+v err=%v", got, err)
	}
}
