package agents

import (
	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

type sessionStub struct {
	actor user.User
	ok    bool
}

func (s *sessionStub) CurrentUser(context.Context) (user.User, bool) { return s.actor, s.ok }
func (*sessionStub) Token(context.Context) string                    { return "" }
func (*sessionStub) SetCookie(context.Context, string, time.Time)    {}
func (*sessionStub) ClearCookie(context.Context)                     {}
func setup(t *testing.T) (*Controller, *store.DB, *sessionStub) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "agents.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &sessionStub{actor: user.User{ID: "admin", Username: "admin", Role: user.RoleAdmin}, ok: true}
	return New(Deps{DB: db, Registry: agentlink.NewRegistry(), Guard: agentlink.NewEnrollmentGuard(), Session: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}), db, s
}
func requireError(t *testing.T, err error, status int, message string) {
	t.Helper()
	var e *controller.Error
	if !errors.As(err, &e) || e.Status != status || e.Message != message {
		t.Fatalf("error=%#v want %d %s", err, status, message)
	}
}
func TestAgentLifecycle(t *testing.T) {
	c, db, _ := setup(t)
	ctx := t.Context()
	empty, err := c.List(ctx, &api.ListReq{})
	if err != nil || empty.Agents == nil || len(empty.Agents) != 0 {
		t.Fatalf("empty=%+v %v", empty, err)
	}
	created, err := c.Create(ctx, &api.CreateReq{Name: "  z-host  ", Note: "  note  "})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Agent.ID
	if created.Agent.Name != "z-host" || created.Agent.Status != "pending" || created.Agent.Note != "note" || created.Agent.Labels == nil || created.Enrollment.AgentID != id || created.Enrollment.Token == "" {
		t.Fatalf("create=%+v", created)
	}
	hash := agentlink.HashEnrollmentToken(created.Enrollment.Token)
	if got, err := db.LookupAgentByTokenHash(ctx, hash); err != nil || got != id {
		t.Fatalf("hash lookup=%q %v", got, err)
	}
	_, err = c.Create(ctx, &api.CreateReq{Name: "z-host"})
	requireError(t, err, 409, "已存在同名的服务器，请换一个名称")
	display, note, address := "  renamed  ", "", "[2001:db8::1]"
	patched, err := c.Patch(ctx, &api.PatchReq{AgentID: id, DisplayName: &display, Note: &note, DirectAddress: &address})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Agent.DisplayName != "renamed" || patched.Agent.Note != "" || patched.Agent.DirectAddress != "2001:db8::1" {
		t.Fatalf("patch=%+v", patched)
	}
	disabled, err := c.Disable(ctx, &api.DisableReq{AgentID: id})
	if err != nil || !disabled.Agent.Disabled {
		t.Fatalf("disable=%+v %v", disabled, err)
	}
	enabled, err := c.Enable(ctx, &api.EnableReq{AgentID: id})
	if err != nil || enabled.Agent.Disabled {
		t.Fatalf("enable=%+v %v", enabled, err)
	}
	reset, err := c.ResetToken(ctx, &api.ResetTokenReq{AgentID: id})
	if err != nil {
		t.Fatal(err)
	}
	if reset.Enrollment.Token == created.Enrollment.Token {
		t.Fatal("token not rotated")
	}
	if _, err := db.LookupAgentByTokenHash(ctx, hash); !errors.Is(err, store.ErrAgentTokenNotFound) {
		t.Fatalf("old token still valid: %v", err)
	}
	now := time.Now().UTC()
	if err := db.CreateComposeStack(ctx, store.ComposeStack{ID: "stack", AgentID: id, Name: "stack", Content: "services: {}", Version: 1, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	deleted, err := c.Delete(ctx, &api.DeleteReq{AgentID: id})
	if err != nil || !deleted.Deleted || deleted.OrphanStacks != 1 {
		t.Fatalf("delete=%+v %v", deleted, err)
	}
	if count, err := db.CountComposeStacks(ctx, id); err != nil || count != 1 {
		t.Fatalf("compose lost %d %v", count, err)
	}
	if has, err := db.HasAgentToken(ctx, id); err != nil || has {
		t.Fatalf("token retained %v %v", has, err)
	}
	_, err = c.Delete(ctx, &api.DeleteReq{AgentID: id})
	requireError(t, err, 404, "服务器不存在")
}
func TestAgentAuthorizationAndViews(t *testing.T) {
	c, db, s := setup(t)
	ctx := t.Context()
	for _, r := range []store.AgentRecord{{ID: "b", Name: "z", DisplayName: "  ", Status: "online"}, {ID: "a", Name: "a", Status: store.StatusPending}} {
		if err := db.UpsertAgent(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	list, err := c.List(ctx, &api.ListReq{})
	if err != nil || len(list.Agents) != 2 || list.Agents[0].ID != "a" || list.Agents[1].Status != "offline" || list.Agents[1].DisplayName != "z" {
		t.Fatalf("views=%+v %v", list, err)
	}
	s.actor.Role = user.RoleUser
	list, err = c.List(ctx, &api.ListReq{})
	if err != nil || len(list.Agents) != 0 {
		t.Fatalf("unauthorized views=%+v %v", list, err)
	}
	_, err = c.Create(ctx, &api.CreateReq{})
	requireError(t, err, 403, "该操作仅管理员可用")
	_, err = c.Patch(ctx, &api.PatchReq{})
	requireError(t, err, 403, "该操作仅管理员可用")
	s.ok = false
	_, err = c.List(ctx, &api.ListReq{})
	requireError(t, err, 401, "登录已失效，请重新登录")
}
func TestAgentViewOnlineOverridesPending(t *testing.T) {
	view := ToAgentView(store.AgentRecord{ID: "x", Status: store.StatusPending}, map[string]struct{}{"x": {}})
	if view.Status != "online" || view.Labels == nil {
		t.Fatalf("view=%+v", view)
	}
}
