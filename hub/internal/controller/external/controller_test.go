package external

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "arkade/hub/api/external/v1"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	compose "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/net/ghttp"
)

type sessionStub struct{ actor user.User }

func (s *sessionStub) CurrentUser(context.Context) (user.User, bool) { return s.actor, true }
func (*sessionStub) Token(context.Context) string                    { return "" }
func (*sessionStub) SetCookie(context.Context, string, time.Time)    {}
func (*sessionStub) ClearCookie(context.Context)                     {}

type fakeOps struct {
	calls        []string
	list         envelope.ComposeListResult
	err          *agentlink.ConnError
	previewBlock bool
	adoptFail    bool
}

func (o *fakeOps) ComposeList(context.Context, string) (envelope.ComposeListResult, *agentlink.ConnError) {
	o.calls = append(o.calls, "list")
	return o.list, o.err
}
func (o *fakeOps) ListContainers(context.Context, string) (envelope.ContainerListResult, *agentlink.ConnError) {
	o.calls = append(o.calls, "containers")
	return envelope.ContainerListResult{Containers: []envelope.ContainerSummary{{ID: "mine", StackID: "ext"}, {ID: "other", StackID: "other"}}}, nil
}
func (o *fakeOps) ComposeSourceRaw(context.Context, string, string) (envelope.ComposeSourceResult, *agentlink.ConnError) {
	o.calls = append(o.calls, "source")
	return envelope.ComposeSourceResult{Name: "ext", Content: "services: {}", ConfigHash: compose.ProjectHash("services: {}", "")}, nil
}
func (o *fakeOps) ComposePreview(_ context.Context, _ string, p envelope.ComposePreviewPayload) (envelope.ComposePreviewResult, *agentlink.ConnError) {
	o.calls = append(o.calls, "preview")
	return envelope.ComposePreviewResult{CanImport: !o.previewBlock, ConfigHash: compose.ProjectHash(p.Content, p.EnvContent), Blockers: []string{}, Warnings: []string{}}, nil
}
func (o *fakeOps) ComposeAdopt(_ context.Context, _ string, p envelope.ComposeAdoptPayload) (envelope.ComposeAdoptResult, *agentlink.ConnError) {
	o.calls = append(o.calls, "adopt")
	if o.adoptFail {
		return envelope.ComposeAdoptResult{}, &agentlink.ConnError{Code: "agent_offline", Message: "offline"}
	}
	return envelope.ComposeAdoptResult{Adopted: true, ConfigHash: compose.ProjectHash(p.Content, p.EnvContent)}, nil
}

func setup(t *testing.T) (*Controller, *store.DB, *fakeOps, *sessionStub) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "external.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertAgent(context.Background(), store.AgentRecord{ID: "agent", Name: "agent", Labels: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	ops := &fakeOps{list: envelope.ComposeListResult{External: []envelope.ComposeProjectSummary{{Name: "ext", Status: "running", Services: []string{"app"}}}}}
	session := &sessionStub{actor: user.User{ID: "admin", Role: user.RoleAdmin}}
	return New(Deps{DB: db, Ops: ops, Session: session, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}), db, ops, session
}
func bodyCtx(body string) context.Context {
	r := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	gf := &ghttp.Request{Request: httpwire.HideRawBody(r)}
	return gf.Context()
}
func jsonBody(t *testing.T, input any) context.Context {
	t.Helper()
	b, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return bodyCtx(string(b))
}
func wantError(t *testing.T, err error, status int, message string) {
	t.Helper()
	var e *controller.Error
	if !errors.As(err, &e) || e.Status != status || e.Message != message {
		t.Fatalf("got %#v want %d %q", err, status, message)
	}
}

func TestExternalInspectionAndDraftCAS(t *testing.T) {
	c, db, ops, _ := setup(t)
	detail, err := c.Detail(context.Background(), &api.DetailReq{AgentID: "agent", Name: "ext"})
	if err != nil || len(detail.Containers) != 1 || detail.Containers[0].ID != "mine" {
		t.Fatalf("detail %+v %v", detail, err)
	}
	config, err := c.Config(context.Background(), &api.ConfigReq{AgentID: "agent", Name: "ext"})
	if err != nil || !config.Readable || config.Content == nil {
		t.Fatalf("config %+v %v", config, err)
	}
	input := api.DraftInput{Content: "services: {}", SourceHash: "baseline"}
	res, err := c.Draft(jsonBody(t, input), &api.DraftReq{AgentID: "agent", Name: "ext"})
	if err != nil || res.Draft.Version != 1 {
		t.Fatalf("draft %+v %v", res, err)
	}
	_, err = c.Draft(jsonBody(t, input), &api.DraftReq{AgentID: "agent", Name: "ext"})
	wantError(t, err, 409, "名称已被占用，请换一个")
	if _, err := db.GetComposeStack(context.Background(), "agent", "ext"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("inspection/draft adopted project")
	}
	for _, call := range ops.calls {
		if call == "adopt" || call == "preview" {
			t.Fatalf("unexpected side effect %s", call)
		}
	}
}

func TestExternalInspectAndPermissionBeforeDecode(t *testing.T) {
	c, _, ops, session := setup(t)
	ops.list = envelope.ComposeListResult{}
	_, err := c.Draft(bodyCtx("invalid"), &api.DraftReq{AgentID: "agent", Name: "ext"})
	wantError(t, err, 404, "引擎中未找到该编排项目")
	ops.calls = nil
	session.actor.Role = user.RoleUser
	_, err = c.Apply(bodyCtx("invalid"), &api.ApplyReq{AgentID: "agent", Name: "ext"})
	wantError(t, err, 403, "没有该服务器的操作权限")
	if len(ops.calls) != 0 {
		t.Fatalf("permission failure called RPC %v", ops.calls)
	}
}

func TestImportAtomicRetryAndEnvNeverReturned(t *testing.T) {
	c, db, ops, _ := setup(t)
	input := api.ImportInput{Name: "imported", Content: "services: {}", EnvContent: "PASSWORD=secret", Confirmation: true}
	input.ExpectedContentHash = compose.ProjectHash(input.Content, input.EnvContent)
	preview, err := c.ImportPreview(jsonBody(t, input), &api.ImportPreviewReq{AgentID: "agent"})
	if err != nil || !preview.CanImport {
		t.Fatalf("preview %+v %v", preview, err)
	}
	ops.adoptFail = true
	res, err := c.Import(jsonBody(t, input), &api.ImportReq{AgentID: "agent"})
	if err != nil || res.SyncOK {
		t.Fatalf("failed adopt %+v %v", res, err)
	}
	rec, err := db.GetComposeStack(context.Background(), "agent", input.Name)
	if err != nil || rec.EnvContent != input.EnvContent || !rec.SyncedAt.IsZero() {
		t.Fatalf("unsynced asset lost %+v %v", rec, err)
	}
	ops.adoptFail = false
	res, err = c.Import(jsonBody(t, input), &api.ImportReq{AgentID: "agent"})
	if err != nil || !res.SyncOK {
		t.Fatalf("retry %+v %v", res, err)
	}
	encoded, _ := json.Marshal(res)
	if strings.Contains(string(encoded), "PASSWORD") || strings.Contains(string(encoded), "secret") {
		t.Fatal("env leaked")
	}
	_, err = c.Import(jsonBody(t, input), &api.ImportReq{AgentID: "agent"})
	wantError(t, err, 409, "名称已被占用，请换一个")
}

func TestExternalApplyUsesPathAndCopyNeverDeploys(t *testing.T) {
	c, db, ops, _ := setup(t)
	input := api.ImportInput{Name: "body-forgery", Content: "services: {}", Confirmation: true, ExpectedContentHash: compose.ProjectHash("services: {}", "")}
	res, err := c.Apply(jsonBody(t, input), &api.ApplyReq{AgentID: "agent", Name: "ext"})
	if err != nil || !res.Adopted || res.Stack.Name != "ext" {
		t.Fatalf("apply %+v %v", res, err)
	}
	if _, err := db.GetComposeStack(context.Background(), "agent", "body-forgery"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("body name controlled identity")
	}
	ops.calls = nil
	copied, err := c.Copy(jsonBody(t, api.CopyInput{TargetAgent: "agent", TargetName: "copied", Content: "services: {}", Confirmation: true}), &api.CopyReq{AgentID: "agent", Name: "ext"})
	if err != nil || copied.Started || copied.DataIncluded || !copied.SourcePreserved {
		t.Fatalf("copy %+v %v", copied, err)
	}
	for _, call := range ops.calls {
		if call != "list" {
			t.Fatalf("copy invoked %s", call)
		}
	}
	rec, err := db.GetComposeStack(context.Background(), "agent", "copied")
	if err != nil || !rec.SyncedAt.IsZero() || rec.EnvContent != "" {
		t.Fatalf("copy deployed %+v %v", rec, err)
	}
}
