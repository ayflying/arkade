package agents

import (
	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/secretbox"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
	"arkade/hub/internal/user"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
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
	db, err := store.Open(t.Context(), testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &sessionStub{actor: user.User{ID: "admin", Username: "admin", Role: user.RoleAdmin}, ok: true}
	return New(Deps{DB: db, Registry: agentlink.NewRegistry(), Guard: agentlink.NewEnrollmentGuard(), Session: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}), db, s
}

// setupWithBox 与 setup 相同，但带上加密器，用于验证「待接入可回看密钥」。
func setupWithBox(t *testing.T) (*Controller, *store.DB, *sessionStub) {
	t.Helper()
	db, err := store.Open(t.Context(), testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &sessionStub{actor: user.User{ID: "admin", Username: "admin", Role: user.RoleAdmin}, ok: true}
	box, err := secretbox.New(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return New(Deps{DB: db, Registry: agentlink.NewRegistry(), Guard: agentlink.NewEnrollmentGuard(), Session: s, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Box: box}), db, s
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
	// 2026-11 起访问地址必须是带协议的完整地址（旧纯主机不再接受）
	display, note, address := "  renamed  ", "", " HTTPS://[2001:0DB8:0:0::1] "
	patched, err := c.Patch(ctx, &api.PatchReq{AgentID: id, DisplayName: &display, Note: &note, DirectAddress: &address})
	if err != nil {
		t.Fatal(err)
	}
	if patched.Agent.DisplayName != "renamed" || patched.Agent.Note != "" || patched.Agent.DirectAddress != "https://[2001:db8::1]" {
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

// TestEnrollmentViewableWhilePending 验证「待接入可反复查看密钥、接入后立刻不可用」。
//
// 这是本次需求的核心不变量：可回读的副本只在从未接入的窗口内存在。
func TestEnrollmentViewableWhilePending(t *testing.T) {
	c, db, _ := setupWithBox(t)
	ctx := t.Context()
	created, err := c.Create(ctx, &api.CreateReq{Name: "peek-host"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Agent.ID

	first, err := c.GetEnrollment(ctx, &api.GetEnrollmentReq{AgentID: id})
	if err != nil {
		t.Fatal(err)
	}
	if first.Enrollment.Token != created.Enrollment.Token || first.Enrollment.AgentID != id {
		t.Fatalf("回读的密钥与创建时不一致: %+v", first.Enrollment)
	}
	// 反复查看必须拿到同一把：不能每次读取都换密钥。
	again, err := c.GetEnrollment(ctx, &api.GetEnrollmentReq{AgentID: id})
	if err != nil || again.Enrollment.Token != first.Enrollment.Token {
		t.Fatalf("第二次读取=%+v %v", again, err)
	}

	// 模拟首次握手成功：状态转为在线并抹掉密文。
	if err := db.UpdateAgentStatus(ctx, id, "online", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	if err := db.ClearAgentTokenCipher(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, err = c.GetEnrollment(ctx, &api.GetEnrollmentReq{AgentID: id})
	requireError(t, err, 409, "该服务器已接入，接入密钥只在添加时展示一次；需要重新获取请重置密钥")

	// 重置后进入"等待重新接入"的窗口，又应该能看。
	if err := db.UpdateAgentStatus(ctx, id, store.StatusPending, 0); err != nil {
		t.Fatal(err)
	}
	reset, err := c.ResetToken(ctx, &api.ResetTokenReq{AgentID: id})
	if err != nil {
		t.Fatal(err)
	}
	afterReset, err := c.GetEnrollment(ctx, &api.GetEnrollmentReq{AgentID: id})
	if err != nil || afterReset.Enrollment.Token != reset.Enrollment.Token {
		t.Fatalf("重置后回读=%+v %v", afterReset, err)
	}
}

// TestEnrollmentViewRequiresCipherAndAdmin 验证没有密文时明确报错，且非管理员被拒。
func TestEnrollmentViewRequiresCipherAndAdmin(t *testing.T) {
	c, _, s := setup(t)
	ctx := t.Context()
	created, err := c.Create(ctx, &api.CreateReq{Name: "no-box-host"})
	if err != nil {
		t.Fatal(err)
	}
	// 没有装配加密器时不保存密文：查看必须给出可操作的提示，而不是返回空密钥。
	_, err = c.GetEnrollment(ctx, &api.GetEnrollmentReq{AgentID: created.Agent.ID})
	requireError(t, err, 409, "该服务器的接入信息已不可查看，请重置密钥")

	s.actor.Role = user.RoleUser
	_, err = c.GetEnrollment(ctx, &api.GetEnrollmentReq{AgentID: created.Agent.ID})
	requireError(t, err, 403, "该操作仅管理员可用")

	_, err = c.GetEnrollment(ctx, &api.GetEnrollmentReq{AgentID: "agent-missing"})
	requireError(t, err, 404, "服务器不存在")
}
