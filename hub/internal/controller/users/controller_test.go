package users

import (
	"arkade/hub/internal/testdb"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	api "arkade/hub/api/users/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

type sessionStub struct {
	u  user.User
	ok bool
}

func (s *sessionStub) CurrentUser(context.Context) (user.User, bool) { return s.u, s.ok }
func (*sessionStub) Token(context.Context) string                    { return "" }
func (*sessionStub) SetCookie(context.Context, string, time.Time)    {}
func (*sessionStub) ClearCookie(context.Context)                     {}

func setup(t *testing.T) (*Controller, *store.DB, *sessionStub) {
	t.Helper()
	db, err := store.Open(context.Background(), testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	settings, err := db.GetSystemSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.PasswordMinLength = 8 // legacy contract fixture; production defaults to 12
	if err := db.UpdateSystemSettings(context.Background(), settings, settings.Version); err != nil {
		t.Fatal(err)
	}
	s := &sessionStub{u: user.User{ID: "operator", Username: "operator", Role: user.RoleAdmin}, ok: true}
	return New(db, s, slog.New(slog.NewTextHandler(io.Discard, nil))), db, s
}

func wantError(t *testing.T, err error, status int, code, message string) {
	t.Helper()
	var actual *controller.Error
	if !errors.As(err, &actual) || actual.Status != status || actual.Code != code || actual.Message != message {
		t.Fatalf("got %#v; want %d %s %q", err, status, code, message)
	}
}

func create(t *testing.T, c *Controller, name, role string) api.UserView {
	t.Helper()
	res, err := c.Create(context.Background(), &api.CreateReq{Username: name, Password: "password123", Role: role})
	if err != nil {
		t.Fatal(err)
	}
	return res.User
}

func TestLifecycleAndSessionRevocation(t *testing.T) {
	c, db, _ := setup(t)
	ctx := context.Background()
	listed, err := c.List(ctx, &api.ListReq{})
	if err != nil || listed.Users == nil || len(listed.Users) != 0 {
		t.Fatalf("empty list: %+v %v", listed, err)
	}
	u := create(t, c, "  Alice  ", "user")
	if u.Username != "alice" || u.DisplayName != "alice" || u.GroupIDs == nil || u.CreatedAt < 1 || u.UpdatedAt != u.CreatedAt {
		t.Fatalf("bad view: %+v", u)
	}
	stored, err := db.GetUserByID(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PasswordHash == "" || stored.PasswordHash == "password123" {
		t.Fatal("password not hashed")
	}
	body, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"password":`) || strings.Contains(string(body), `"password_hash":`) || strings.Contains(string(body), stored.PasswordHash) {
		t.Fatal("password leaked")
	}
	_, err = c.Create(ctx, &api.CreateReq{Username: "ALICE", Password: "password123", Role: "user"})
	wantError(t, err, 409, "conflict", "名称已被占用，请换一个")
	if err := db.CreateSession(ctx, "session1", u.ID, 1, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	_, err = c.ResetPassword(ctx, &api.ResetPasswordReq{UserID: u.ID, Password: "newpassword123"})
	if !errors.Is(err, controller.ErrNoContent) {
		t.Fatalf("reset: %v", err)
	}
	if _, err := db.SessionUserID(ctx, "session1", time.Now().Unix()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("session survived: %v", err)
	}
	if err := db.CreateSession(ctx, "session2", u.ID, 1, time.Now().Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	_, err = c.Delete(ctx, &api.DeleteReq{UserID: u.ID})
	if !errors.Is(err, controller.ErrNoContent) {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.SessionUserID(ctx, "session2", time.Now().Unix()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted session survived: %v", err)
	}
	_, err = c.Delete(ctx, &api.DeleteReq{UserID: u.ID})
	wantError(t, err, 404, "not_found", "用户不存在")
}

func TestPatchNullAndAdminGuards(t *testing.T) {
	c, _, s := setup(t)
	ctx := context.Background()
	admin := create(t, c, "admin", "admin")
	_, err := c.Update(ctx, &api.UpdateReq{UserID: admin.ID})
	wantError(t, err, 400, "bad_request", "没有需要修改的字段")
	_, err = c.Update(ctx, &api.UpdateReq{UserID: admin.ID, Disabled: api.Optional[bool]{Set: true, Value: true}})
	wantError(t, err, 403, "forbidden", "这是当前唯一有效的管理员，不能降级或禁用；请先指定另一位管理员")
	_, err = c.Delete(ctx, &api.DeleteReq{UserID: admin.ID})
	wantError(t, err, 403, "forbidden", "这是当前唯一有效的管理员，不能删除；请先指定另一位管理员")
	s.u.ID = admin.ID
	_, err = c.Delete(ctx, &api.DeleteReq{UserID: admin.ID})
	wantError(t, err, 403, "forbidden", "不能删除当前登录的账号")
	create(t, c, "other", "admin")
	_, err = c.Update(ctx, &api.UpdateReq{UserID: admin.ID, Disabled: api.Optional[bool]{Set: true, Value: true}})
	if err != nil {
		t.Fatal(err)
	}
	var req api.UpdateReq
	if err := json.Unmarshal([]byte(`{"disabled":null}`), &req); err != nil {
		t.Fatal(err)
	}
	req.UserID = admin.ID
	res, err := c.Update(ctx, &req)
	if err != nil || res.User.Disabled {
		t.Fatalf("null should re-enable: %+v %v", res, err)
	}
	// Self-demotion is permitted when another active administrator exists.
	_, err = c.Update(ctx, &api.UpdateReq{UserID: admin.ID, Role: api.Optional[string]{Set: true, Value: "user"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAuthorizationAndValidationOrder(t *testing.T) {
	c, _, s := setup(t)
	ctx := context.Background()
	s.ok = false
	_, err := c.List(ctx, &api.ListReq{})
	wantError(t, err, 401, "unauthorized", "登录已失效，请重新登录")
	s.ok = true
	s.u.Role = user.RoleUser
	_, err = c.Create(ctx, &api.CreateReq{})
	wantError(t, err, 403, "forbidden", "该操作仅管理员可用")
	s.u.Role = user.RoleAdmin
	_, err = c.Create(ctx, &api.CreateReq{})
	wantError(t, err, 400, "bad_request", user.ValidateUsername("").Error())
	_, err = c.Update(ctx, &api.UpdateReq{UserID: "missing", DisplayName: api.Optional[string]{Set: true}})
	wantError(t, err, 404, "not_found", "用户不存在")
	_, err = c.ResetPassword(ctx, &api.ResetPasswordReq{UserID: "missing"})
	wantError(t, err, 400, "bad_request", user.ValidatePassword("").Error())
}
