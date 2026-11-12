package stackmigration

import (
	"arkade/hub/internal/testdb"
	"context"
	"errors"
	"testing"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/service/compose"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

type fakeOps struct {
	Ops
	plan       envelope.ComposeMigrationPlanResult
	list       envelope.ComposeListResult
	imported   int
	importHook func()
	importErr  *agentlink.ConnError
}

func (f *fakeOps) ComposeList(context.Context, string) (envelope.ComposeListResult, *agentlink.ConnError) {
	return f.list, nil
}
func (f *fakeOps) ComposeMigrationPlan(context.Context, string, envelope.ComposeMigrationPlanPayload) (envelope.ComposeMigrationPlanResult, *agentlink.ConnError) {
	return f.plan, nil
}
func (f *fakeOps) ComposeImport(_ context.Context, _ string, p envelope.ComposeImportPayload) (envelope.ComposeImportResult, *agentlink.ConnError) {
	f.imported++
	if p.Overwrite {
		panic("overwrite enabled")
	}
	if f.importHook != nil {
		f.importHook()
	}
	return envelope.ComposeImportResult{Name: p.Name, ConfigHash: p.ExpectHash}, f.importErr
}
func setup(t *testing.T) (*Backend, *fakeOps, context.Context) {
	t.Helper()
	db, err := store.Open(context.Background(), testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertAgent(context.Background(), store.AgentRecord{ID: "source", Name: "source"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertAgent(context.Background(), store.AgentRecord{ID: "target", Name: "target"}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateUser(context.Background(), user.User{ID: "admin", Username: "admin", PasswordHash: "x", Role: user.RoleAdmin}); err != nil {
		t.Fatal(err)
	}
	ops := &fakeOps{}
	return New(Deps{DB: db, Ops: ops}), ops, stackmigrate.WithOwner(context.Background(), "admin")
}
func TestSourcePlanUsesRealEnvAndRejectsDrift(t *testing.T) {
	b, ops, ctx := setup(t)
	p := envelope.ComposeMigrationPlanResult{Name: "web", Config: "services: {}", Env: "SECRET=value", Images: []string{"b", "a", "a"}, Volumes: []envelope.ComposeMigrationVolume{{Name: "web_data", Kind: "named"}, {Name: "shared", Kind: "external"}}}
	p.ConfigHash = compose.ProjectHash(p.Config, p.Env)
	ops.plan = p
	if err := b.deps.DB.CreateComposeStack(ctx, store.ComposeStack{ID: "src", AgentID: "source", Name: "web", Content: p.Config, EnvContent: p.Env, ConfigHash: p.ConfigHash, Version: 1}); err != nil {
		t.Fatal(err)
	}
	config, env, hash, err := b.ReadCompose(ctx, "source", "web")
	if err != nil || config != p.Config || env != p.Env || hash != p.ConfigHash {
		t.Fatal("lost real source inputs", err)
	}
	images, err := b.ImagesOf(ctx, "source", "web")
	if err != nil || len(images) != 2 || images[0] != "a" {
		t.Fatal(images, err)
	}
	plan, err := b.VolumesOf(ctx, "source", "web")
	if err != nil || len(plan.Volumes) != 2 || len(plan.External) != 1 {
		t.Fatal(plan, err)
	}
	ops.plan.Env = "changed"
	if _, _, _, err := b.ReadCompose(ctx, "source", "web"); err == nil {
		t.Fatal("accepted source drift")
	}
}
func TestTargetCollisionAndCreateOnlyAuthority(t *testing.T) {
	b, ops, ctx := setup(t)
	ops.list.External = []envelope.ComposeProjectSummary{{Name: "web"}}
	config := "services: {}"
	hash := compose.ProjectHash(config, "")
	if err := b.ImportCompose(ctx, "target", "web", config, "", hash); err == nil || ops.imported != 0 {
		t.Fatal("overwrote external target")
	}
	ops.list.External = nil
	if err := b.ImportCompose(ctx, "target", "web", config, "", hash); err != nil {
		t.Fatal(err)
	}
	record, err := b.deps.DB.GetComposeStack(ctx, "target", "web")
	if err != nil || record.ConfigHash != hash || record.SyncedAt.IsZero() {
		t.Fatal(record, err)
	}
	if err := b.ImportCompose(ctx, "target", "web", config, "", hash); err == nil || ops.imported != 1 {
		t.Fatal("overwrote saved target")
	}
}
func TestTargetConcurrentInsertReportsWrittenResidual(t *testing.T) {
	b, ops, ctx := setup(t)
	config := "services: {}"
	hash := compose.ProjectHash(config, "")
	ops.importHook = func() {
		if err := b.deps.DB.CreateComposeStack(ctx, store.ComposeStack{ID: "racer", AgentID: "target", Name: "web", Content: "other", Version: 1}); err != nil {
			t.Fatal(err)
		}
	}
	err := b.ImportCompose(ctx, "target", "web", config, "", hash)
	var partial *stackmigrate.ResidualError
	if !errors.As(err, &partial) || !partial.Residual.ComposeWritten {
		t.Fatalf("missing residual: %v", err)
	}
	record, err := b.deps.DB.GetComposeStack(ctx, "target", "web")
	if err != nil || record.Content != "other" {
		t.Fatal("overwrote concurrent authority", record, err)
	}
}
func TestMissingOwnerFailsClosed(t *testing.T) {
	b, _, _ := setup(t)
	ok, err := b.CanManage(context.Background(), "source", "web")
	if err != nil || ok {
		t.Fatal(ok, err)
	}
}
