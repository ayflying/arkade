package httpx

import (
	"context"
	"testing"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

type autoBackendFake struct {
	calls     []string
	content   string
	blockers  []string
	badHash   bool
	adoptFail bool
}

func (f *autoBackendFake) ComposeList(context.Context, string) (envelope.ComposeListResult, *agentlink.ConnError) {
	f.calls = append(f.calls, "list")
	return envelope.ComposeListResult{External: []envelope.ComposeProjectSummary{{Name: "auto-test"}}}, nil
}
func (f *autoBackendFake) ComposeSource(context.Context, string, string) (envelope.ComposeSourceResult, *agentlink.ConnError) {
	f.calls = append(f.calls, "source")
	h := composeProjectHash(f.content, "")
	if f.badHash {
		h = "bad"
	}
	return envelope.ComposeSourceResult{Name: "auto-test", Content: f.content, ConfigHash: h, Blockers: f.blockers}, nil
}
func (f *autoBackendFake) ComposePreview(_ context.Context, _ string, p envelope.ComposePreviewPayload) (envelope.ComposePreviewResult, *agentlink.ConnError) {
	f.calls = append(f.calls, "preview")
	return envelope.ComposePreviewResult{CanImport: true, ConfigHash: composeProjectHash(p.Content, "")}, nil
}
func (f *autoBackendFake) ComposeAdopt(_ context.Context, _ string, p envelope.ComposeAdoptPayload) (envelope.ComposeAdoptResult, *agentlink.ConnError) {
	f.calls = append(f.calls, "adopt")
	return envelope.ComposeAdoptResult{Adopted: !f.adoptFail, ConfigHash: p.ExpectedContentHash}, nil
}
func TestAutoAdoptSafeRegistrationAndIdempotence(t *testing.T) {
	e := newTestEnv(t)
	ctx := context.Background()
	f := &autoBackendFake{content: "services:\n  app:\n    image: alpine:3.20\n"}
	a := newAutoAdopter(e.db, f, nil)
	a.scan(ctx, "test-agent")
	r, err := e.db.GetComposeStack(ctx, "test-agent", "auto-test")
	if err != nil {
		t.Fatal(err)
	}
	if r.Content != f.content || r.SyncedAt.IsZero() || r.Version != 1 {
		t.Fatalf("incorrect registration: %+v", r)
	}
	if a.state("test-agent", "auto-test").Status != "adopted" {
		t.Fatal("not adopted")
	}
	before := len(f.calls)
	a.scan(ctx, "test-agent")
	if len(f.calls) != before+1 {
		t.Fatalf("repeat mutated: %v", f.calls)
	}
}
func TestAutoAdoptBlockedOrHashMismatchNeverWrites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		blockers []string
		badHash  bool
	}{{"unsafe", []string{"包含外部文件依赖"}, false}, {"changed", nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t)
			f := &autoBackendFake{content: "services: {}", blockers: tc.blockers, badHash: tc.badHash}
			a := newAutoAdopter(e.db, f, nil)
			a.scan(context.Background(), "a")
			rows, err := e.db.ListComposeStacks(context.Background(), "a")
			if err != nil || len(rows) != 0 {
				t.Fatal("blocked project persisted")
			}
			if len(f.calls) != 2 {
				t.Fatalf("unsafe RPC: %v", f.calls)
			}
			if a.state("a", "auto-test").Status != "skipped" {
				t.Fatal("missing reason")
			}
		})
	}
}
func TestAutoAdoptRetryAndNeverOverwriteDifferentConfig(t *testing.T) {
	e := newTestEnv(t)
	f := &autoBackendFake{content: "services: {}", adoptFail: true}
	a := newAutoAdopter(e.db, f, nil)
	ctx := context.Background()
	a.scan(ctx, "a")
	r, err := e.db.GetComposeStack(ctx, "a", "auto-test")
	if err != nil || !r.SyncedAt.IsZero() {
		t.Fatal("failed adopt marked synced")
	}
	f.adoptFail = false
	a.scan(ctx, "a")
	r, err = e.db.GetComposeStack(ctx, "a", "auto-test")
	if err != nil || r.SyncedAt.IsZero() {
		t.Fatal("retry failed")
	}
	// 未同步同名记录内容不同，自动发现绝不能替换用户已保存内容。
	other := store.ComposeStack{ID: "different", AgentID: "b", Name: "auto-test", Content: "user-data", ConfigHash: "different", Version: 4, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := e.db.CreateComposeStack(ctx, other); err != nil {
		t.Fatal(err)
	}
	before := len(f.calls)
	a.scan(ctx, "b")
	if len(f.calls) != before+2 {
		t.Fatal("different config reached adopt")
	}
	got, _ := e.db.GetComposeStack(ctx, "b", "auto-test")
	if got.Content != "user-data" || got.Version != 4 {
		t.Fatal("overwrote user record")
	}
}
