package external

import (
	"context"
	"errors"
	"testing"

	api "arkade/hub/api/external/v1"
	"arkade/hub/internal/store"
)

func TestApplyPreviewOnlyAndRejectedImportIsAtomic(t *testing.T) {
	c, db, ops, _ := setup(t)
	preview, err := c.ApplyPreview(jsonBody(t, api.ApplyPreviewInput{Content: "services: {}"}), &api.ApplyPreviewReq{AgentID: "agent", Name: "ext"})
	if err != nil || !preview.CanImport {
		t.Fatalf("preview %+v %v", preview, err)
	}
	if _, err := db.GetComposeStack(context.Background(), "agent", "ext"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("preview registered project")
	}
	for _, call := range ops.calls {
		if call != "list" && call != "preview" {
			t.Fatalf("preview side effect %s", call)
		}
	}
	ops.previewBlock = true
	input := api.ImportInput{Name: "blocked", Content: "services: {}", ExpectedContentHash: preview.ConfigHash, Confirmation: true}
	_, err = c.Import(jsonBody(t, input), &api.ImportReq{AgentID: "agent"})
	wantError(t, err, 409, "当前项目不满足安全导入条件，请重新预检并解决阻塞项")
	if _, err := db.GetComposeStack(context.Background(), "agent", "blocked"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("blocked import wrote DB")
	}
}

func TestStrictFirstJSONAndCaseDuplicateResolution(t *testing.T) {
	c, db, _, _ := setup(t)
	_, err := c.Draft(bodyCtx(`{"content":"services: first","CONTENT":"services: last","source_hash":"base"} ignored suffix`), &api.DraftReq{AgentID: "agent", Name: "ext"})
	if err != nil {
		t.Fatal(err)
	}
	draft, err := db.GetExternalComposeDraft(context.Background(), "agent", "ext")
	if err != nil || draft.Content != "services: last" {
		t.Fatalf("encoding/json semantics lost: %+v %v", draft, err)
	}
}
