package stacks

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	comp "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"encoding/json"
	"errors"
	"github.com/gogf/gf/v2/net/ghttp"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sessionStub struct{ u user.User }

func (s *sessionStub) CurrentUser(context.Context) (user.User, bool) { return s.u, true }
func (*sessionStub) Token(context.Context) string                    { return "" }
func (*sessionStub) SetCookie(context.Context, string, time.Time)    {}
func (*sessionStub) ClearCookie(context.Context)                     {}
func setup(t *testing.T) (*Controller, *store.DB, *sessionStub) {
	t.Helper()
	db, e := store.Open(context.Background(), filepath.Join(t.TempDir(), "stack.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	if e := db.UpsertAgent(context.Background(), store.AgentRecord{ID: "agent", Name: "agent", Labels: map[string]string{}}); e != nil {
		t.Fatal(e)
	}
	s := &sessionStub{u: user.User{ID: "admin", Role: user.RoleAdmin}}
	return New(Deps{DB: db, Ops: agentlink.NewOps(agentlink.NewRegistry()), Session: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}), db, s
}
func bodyCtx(body string) context.Context {
	r := httptest.NewRequest("POST", "http://localhost/", strings.NewReader(body))
	gf := &ghttp.Request{Request: httpwire.HideRawBody(r)}
	return gf.Context()
}
func wantError(t *testing.T, e error, status int, message string) {
	t.Helper()
	var actual *controller.Error
	if !errors.As(e, &actual) || actual.Status != status || actual.Message != message {
		t.Fatalf("got %#v want %d %s", e, status, message)
	}
}
func TestCreateUpdateCASAndOfflineRetention(t *testing.T) {
	c, db, _ := setup(t)
	r := &api.CreateReq{AgentID: "agent"}
	created, e := c.Create(bodyCtx(`{"name":"app","content":"services: {}","env_content":"SECRET=hidden"}`), r)
	if e != nil || created.SyncOK || created.Result != nil {
		t.Fatalf("create %+v %v", created, e)
	}
	if created.SyncHint != "编排已保存，但 Agent 当前离线，稍后可重新下发" {
		t.Fatal(created.SyncHint)
	}
	encoded, _ := json.Marshal(created)
	if strings.Contains(string(encoded), "SECRET") || strings.Contains(string(encoded), "hidden") {
		t.Fatal("env leaked")
	}
	rec, e := db.GetComposeStack(context.Background(), "agent", "app")
	if e != nil || rec.EnvContent != "SECRET=hidden" || rec.ConfigHash != comp.ProjectHash(rec.Content, rec.EnvContent) {
		t.Fatalf("stored %+v %v", rec, e)
	}
	updated, e := c.Update(bodyCtx(`{"content":"services: {app: {}}","version":1}`), &api.UpdateReq{AgentID: "agent", Name: "app"})
	if e != nil || updated.Stack.Version != 2 || updated.SyncOK {
		t.Fatalf("update %+v %v", updated, e)
	}
	rec, _ = db.GetComposeStack(context.Background(), "agent", "app")
	if rec.EnvContent != "SECRET=hidden" {
		t.Fatal("omitted env lost")
	}
	_, e = c.Update(bodyCtx(`{"content":"services: {}","version":1}`), &api.UpdateReq{AgentID: "agent", Name: "app"})
	wantError(t, e, 409, "编排内容已被他人修改，请刷新后重新编辑（当前版本与提交的 version 不一致）")
	_, e = c.Delete(context.Background(), &api.DeleteReq{AgentID: "agent", Name: "app", RemoveVolumes: "invalid"})
	wantError(t, e, 400, "remove_volumes必须是true或false")
	_, e = c.Delete(context.Background(), &api.DeleteReq{AgentID: "agent", Name: "app"})
	wantError(t, e, 503, "删除未完成，编排记录已保留：目标 Agent 不在线，无法下发操作")
	if _, e := db.GetComposeStack(context.Background(), "agent", "app"); e != nil {
		t.Fatal("offline deleted record")
	}
}
func TestReadOfflineAndValidationOrder(t *testing.T) {
	c, _, s := setup(t)
	_, e := c.Create(bodyCtx(`{"name":"app","content":"services: {}"}`), &api.CreateReq{AgentID: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	list, e := c.List(context.Background(), &api.ListReq{AgentID: "agent"})
	if e != nil || list.AgentOnline || list.Orphans != nil || list.ExternalStacks != nil || len(list.Stacks) != 1 {
		t.Fatalf("list %+v %v", list, e)
	}
	detail, e := c.Detail(context.Background(), &api.DetailReq{AgentID: "agent", Name: "app"})
	if e != nil || detail.Containers == nil || detail.ContainersError == "" {
		t.Fatalf("detail %+v %v", detail, e)
	}
	_, e = c.Logs(context.Background(), &api.LogsReq{AgentID: "agent", Name: "app", Tail: "2001"})
	wantError(t, e, 400, "tail 必须是 1 到 2000 之间的整数")
	_, e = c.Update(bodyCtx("broken"), &api.UpdateReq{AgentID: "agent", Name: "../x"})
	wantError(t, e, 400, comp.ValidateName("../x").Error())
	s.u.Role = user.RoleUser
	_, e = c.Create(bodyCtx("broken"), &api.CreateReq{AgentID: "agent"})
	wantError(t, e, 403, "没有该服务器的操作权限")
}
