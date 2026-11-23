package agents

import (
	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
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
func TestAgentHealthFollowsListPermission(t *testing.T) {
	c, db, session := setup(t)
	ctx := t.Context()
	if err := db.UpsertAgent(ctx, store.AgentRecord{ID: "health-node", Name: "health-node", Status: "online"}); err != nil {
		t.Fatal(err)
	}
	if err := c.d.Registry.RegisterLocal("health-node", embeddedExecutor{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.d.Registry.RemoveLocal("health-node") })
	cpu := 20.0
	c.d.Registry.LocalHealthRecorder("health-node")(false, &envelope.HostMetrics{CPUPercent: &cpu})
	list, err := c.List(ctx, &api.ListReq{})
	if err != nil || len(list.Agents) != 1 {
		t.Fatalf("admin list=%+v err=%v", list, err)
	}
	view := list.Agents[0]
	if view.SampledAt <= 0 || view.RuntimeOK == nil || *view.RuntimeOK || view.HostMetrics == nil {
		t.Fatalf("missing health fields: %+v", view)
	}
	session.actor.Role = user.RoleUser
	list, err = c.List(ctx, &api.ListReq{})
	if err != nil || len(list.Agents) != 0 {
		t.Fatalf("ungrouped user's list=%+v err=%v", list, err)
	}
}

func TestAgentViewOnlineOverridesPending(t *testing.T) {
	view := ToAgentView(store.AgentRecord{ID: "x", Status: store.StatusPending}, map[string]struct{}{"x": {}})
	if view.Status != "online" || view.Labels == nil {
		t.Fatalf("view=%+v", view)
	}
}

// TestRenewEnrollmentIssuesFreshToken 验证「每次换发都是新密钥，旧密钥立即作废」。
//
// 这是本次需求的核心不变量：库里不留明文，想看只能换发；而换发只在
// 从未接入时允许，因此不会打断任何在跑的连接。
func TestRenewEnrollmentIssuesFreshToken(t *testing.T) {
	c, db, _ := setup(t)
	ctx := t.Context()
	created, err := c.Create(ctx, &api.CreateReq{Name: "renew-host"})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Agent.ID

	first, err := c.RenewEnrollment(ctx, &api.RenewEnrollmentReq{AgentID: id})
	if err != nil {
		t.Fatal(err)
	}
	if first.Enrollment.Token == created.Enrollment.Token || first.Enrollment.AgentID != id {
		t.Fatalf("换发后密钥应当变化: %+v", first.Enrollment)
	}
	// 旧密钥必须立刻失效，新密钥可解析到同一条记录。
	if _, err := db.LookupAgentByTokenHash(ctx, agentlink.HashEnrollmentToken(created.Enrollment.Token)); !errors.Is(err, store.ErrAgentTokenNotFound) {
		t.Fatalf("旧密钥仍然有效: %v", err)
	}
	if got, err := db.LookupAgentByTokenHash(ctx, agentlink.HashEnrollmentToken(first.Enrollment.Token)); err != nil || got != id {
		t.Fatalf("新密钥解析失败: %q %v", got, err)
	}
	// 再换一次又是新的：接口语义是"发一把新的"，不是"回看同一把"。
	second, err := c.RenewEnrollment(ctx, &api.RenewEnrollmentReq{AgentID: id})
	if err != nil || second.Enrollment.Token == first.Enrollment.Token {
		t.Fatalf("第二次换发=%+v %v", second, err)
	}

	// 一旦接入成功，就不再允许换发（避免把在线服务器踢下线）。
	if err := db.UpdateAgentStatus(ctx, id, "online", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	_, err = c.RenewEnrollment(ctx, &api.RenewEnrollmentReq{AgentID: id})
	requireError(t, err, 409, "该服务器已接入，接入密钥只在添加时展示一次；需要重新获取请重置密钥")
}

// TestRenewEnrollmentGuards 验证换发的权限、内置本机与不存在记录的处理。
func TestRenewEnrollmentGuards(t *testing.T) {
	c, _, s := setup(t)
	ctx := t.Context()
	created, err := c.Create(ctx, &api.CreateReq{Name: "guarded-host"})
	if err != nil {
		t.Fatal(err)
	}

	s.actor.Role = user.RoleUser
	_, err = c.RenewEnrollment(ctx, &api.RenewEnrollmentReq{AgentID: created.Agent.ID})
	requireError(t, err, 403, "该操作仅管理员可用")

	s.actor.Role = user.RoleAdmin
	_, err = c.RenewEnrollment(ctx, &api.RenewEnrollmentReq{AgentID: "agent-missing"})
	requireError(t, err, 404, "服务器不存在")

	// 内置本机由 Hub 配置托管：换发必须在查库之前就被拒，
	// 而不是因为"库里没有这条记录"报一个误导人的 404。
	local, _, _ := setup(t)
	if err := local.d.Registry.ReserveLocalID(store.LocalAgentID); err != nil {
		t.Fatal(err)
	}
	_, err = local.RenewEnrollment(ctx, &api.RenewEnrollmentReq{AgentID: store.LocalAgentID})
	requireError(t, err, 400, "本机由 Hub 配置 local.enabled 管理，不支持删除、重置密钥或启用禁用")
}
