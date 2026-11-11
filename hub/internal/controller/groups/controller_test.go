package groups

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	api "arkade/hub/api/groups/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

type sessionStub struct {
	current user.User
	ok      bool
}

func (s *sessionStub) CurrentUser(context.Context) (user.User, bool) { return s.current, s.ok }
func (*sessionStub) Token(context.Context) string                    { return "" }
func (*sessionStub) SetCookie(context.Context, string, time.Time)    {}
func (*sessionStub) ClearCookie(context.Context)                     {}

func setup(t *testing.T) (*Controller, *store.DB, *sessionStub) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	session := &sessionStub{current: user.User{Username: "admin", Role: user.RoleAdmin}, ok: true}
	return New(db, session, slog.New(slog.NewTextHandler(io.Discard, nil))), db, session
}
func checkError(t *testing.T, err error, status int, message string) {
	t.Helper()
	var e *controller.Error
	if !errors.As(err, &e) || e.Status != status || e.Message != message {
		t.Fatalf("error = %#v; want %d %q", err, status, message)
	}
}
func create(t *testing.T, c *Controller, name string) api.GroupView {
	t.Helper()
	res, err := c.Create(context.Background(), &api.CreateReq{Name: name, Description: "描述"})
	if err != nil {
		t.Fatal(err)
	}
	return res.Group
}
func patch(t *testing.T, id, body string) *api.UpdateReq {
	t.Helper()
	req := &api.UpdateReq{GroupID: id}
	if err := json.Unmarshal([]byte(body), req); err != nil {
		t.Fatal(err)
	}
	return req
}

func TestGroupCRUD(t *testing.T) {
	c, _, _ := setup(t)
	ctx := context.Background()
	list, err := c.List(ctx, &api.ListReq{})
	if err != nil || list.Groups == nil || len(list.Groups) != 0 {
		t.Fatalf("empty list: %+v %v", list, err)
	}
	g := create(t, c, "  DEV中文  ")
	if g.Name != "DEV中文" || !strings.HasPrefix(g.ID, "g-") || len(g.ID) != 18 || g.CreatedAt <= 0 || g.MemberCount != 0 || g.GrantCount != 0 {
		t.Fatalf("group %+v", g)
	}
	_, err = c.Create(ctx, &api.CreateReq{Name: "DEV中文"})
	checkError(t, err, 409, "名称已被占用，请换一个")
	_, err = c.Create(ctx, &api.CreateReq{Name: " "})
	checkError(t, err, 400, "用户组名不能为空")
	_, err = c.Create(ctx, &api.CreateReq{Name: "x", Description: strings.Repeat("x", 256)})
	checkError(t, err, 400, "描述不能超过 255 个字符")
	_, err = c.Update(ctx, patch(t, g.ID, `{}`))
	checkError(t, err, 400, "没有需要修改的字段")
	_, err = c.Update(ctx, patch(t, g.ID, `{"name":null}`))
	checkError(t, err, 400, "用户组名不能为空")
	updated, err := c.Update(ctx, patch(t, g.ID, `{"description":null}`))
	if err != nil || updated.Group.Description != "" || updated.Group.Name != g.Name {
		t.Fatalf("null description: %+v %v", updated, err)
	}
	updated, err = c.Update(ctx, patch(t, g.ID, `{"name":"DEV中文"}`))
	if err != nil || updated.Group.Name != g.Name {
		t.Fatalf("unchanged update: %+v %v", updated, err)
	}
	create(t, c, "other")
	_, err = c.Update(ctx, patch(t, g.ID, `{"name":"other"}`))
	checkError(t, err, 409, "名称已被占用，请换一个")
	_, err = c.Delete(ctx, &api.DeleteReq{GroupID: g.ID})
	if !errors.Is(err, controller.ErrNoContent) {
		t.Fatal(err)
	}
	_, err = c.Delete(ctx, &api.DeleteReq{GroupID: g.ID})
	checkError(t, err, 404, "用户组不存在")
}

func TestMembersReplaceAndDeleteKeepsUsers(t *testing.T) {
	c, db, _ := setup(t)
	ctx := context.Background()
	g := create(t, c, "members")
	for _, u := range []user.User{
		{ID: "u-a", Username: "alpha", DisplayName: "A", Role: user.RoleAdmin, Disabled: true},
		{ID: "u-b", Username: "beta", DisplayName: "B", Role: user.RoleUser},
	} {
		if err := db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	res, err := c.SetMembers(ctx, &api.SetMembersReq{GroupID: g.ID, UserIDs: []string{"u-b", "u-a", "u-a"}})
	if err != nil || len(res.Members) != 2 || res.Members[0].ID != "u-a" || !res.Members[0].Disabled || res.Members[0].Role != "admin" {
		t.Fatalf("members %+v %v", res, err)
	}
	_, err = c.SetMembers(ctx, &api.SetMembersReq{GroupID: g.ID, UserIDs: []string{"ghost", "ghost"}})
	checkError(t, err, 400, "以下用户不存在: [ghost]")
	listed, err := c.ListMembers(ctx, &api.ListMembersReq{GroupID: g.ID})
	if err != nil || len(listed.Members) != 2 {
		t.Fatalf("invalid input changed members: %+v %v", listed, err)
	}
	res, err = c.SetMembers(ctx, &api.SetMembersReq{GroupID: g.ID, UserIDs: []string{"u-b"}})
	if err != nil || len(res.Members) != 1 || res.Members[0].ID != "u-b" {
		t.Fatalf("replace %+v %v", res, err)
	}
	res, err = c.SetMembers(ctx, &api.SetMembersReq{GroupID: g.ID})
	if err != nil || res.Members == nil || len(res.Members) != 0 {
		t.Fatalf("clear %+v %v", res, err)
	}
	_, _ = c.SetMembers(ctx, &api.SetMembersReq{GroupID: g.ID, UserIDs: []string{"u-a"}})
	_, err = c.Delete(ctx, &api.DeleteReq{GroupID: g.ID})
	if !errors.Is(err, controller.ErrNoContent) {
		t.Fatal(err)
	}
	if _, err := db.GetUserByID(ctx, "u-a"); err != nil {
		t.Fatal("delete removed user:", err)
	}
}

func TestGrantsReplaceAndValidation(t *testing.T) {
	c, _, _ := setup(t)
	ctx := context.Background()
	g := create(t, c, "grants")
	res, err := c.SetGrants(ctx, &api.SetGrantsReq{GroupID: g.ID, Grants: []api.GrantInput{
		{AgentID: "offline-b", Permission: "manage"}, {AgentID: "offline-a", Permission: "view"}, {AgentID: "offline-b", Permission: "operate"},
	}})
	if err != nil || len(res.Grants) != 2 || res.Grants[0].AgentID != "offline-a" || res.Grants[1].Permission != "operate" {
		t.Fatalf("grants %+v %v", res, err)
	}
	for _, permission := range []string{"none", "View", "", "admin", "manage "} {
		_, err = c.SetGrants(ctx, &api.SetGrantsReq{GroupID: g.ID, Grants: []api.GrantInput{{AgentID: "x", Permission: permission}}})
		checkError(t, err, 400, "第 1 条授权的 permission 必须是 view、operate 或 manage，实际为 "+strconvQuote(permission))
	}
	_, err = c.SetGrants(ctx, &api.SetGrantsReq{GroupID: g.ID, Grants: []api.GrantInput{{Permission: "view"}}})
	checkError(t, err, 400, "第 1 条授权缺少 agent_id")
	listed, err := c.ListGrants(ctx, &api.ListGrantsReq{GroupID: g.ID})
	if err != nil || len(listed.Grants) != 2 {
		t.Fatalf("invalid grants mutated state %+v %v", listed, err)
	}
	updated, err := c.Update(ctx, patch(t, g.ID, `{"description":"new"}`))
	if err != nil || updated.Group.GrantCount != 2 {
		t.Fatalf("count %+v %v", updated, err)
	}
	res, err = c.SetGrants(ctx, &api.SetGrantsReq{GroupID: g.ID, Grants: []api.GrantInput{{AgentID: "new", Permission: "manage"}}})
	if err != nil || len(res.Grants) != 1 || res.Grants[0].AgentID != "new" {
		t.Fatalf("replace %+v %v", res, err)
	}
	res, err = c.SetGrants(ctx, &api.SetGrantsReq{GroupID: g.ID})
	if err != nil || res.Grants == nil || len(res.Grants) != 0 {
		t.Fatalf("clear %+v %v", res, err)
	}
}
func strconvQuote(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestAllMethodsEnforceAdminAndMissingGroup(t *testing.T) {
	c, _, session := setup(t)
	ctx := context.Background()
	calls := []func() error{
		func() error { _, e := c.List(ctx, &api.ListReq{}); return e },
		func() error { _, e := c.Create(ctx, &api.CreateReq{Name: "x"}); return e },
		func() error { _, e := c.Update(ctx, patch(t, "ghost", `{"name":"x"}`)); return e },
		func() error { _, e := c.Delete(ctx, &api.DeleteReq{GroupID: "ghost"}); return e },
		func() error { _, e := c.ListMembers(ctx, &api.ListMembersReq{GroupID: "ghost"}); return e },
		func() error {
			_, e := c.SetMembers(ctx, &api.SetMembersReq{GroupID: "ghost", UserIDs: []string{"missing"}})
			return e
		},
		func() error { _, e := c.ListGrants(ctx, &api.ListGrantsReq{GroupID: "ghost"}); return e },
		func() error {
			_, e := c.SetGrants(ctx, &api.SetGrantsReq{GroupID: "ghost", Grants: []api.GrantInput{{}}})
			return e
		},
	}
	session.ok = false
	for _, call := range calls {
		checkError(t, call(), 401, "登录已失效，请重新登录")
	}
	session.ok = true
	session.current.Role = user.RoleUser
	for _, call := range calls {
		checkError(t, call(), 403, "该操作仅管理员可用")
	}
	session.current.Role = user.RoleAdmin
	for _, call := range calls[2:] {
		checkError(t, call(), 404, "用户组不存在")
	}
}
