package users

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	api "arkade/hub/api/users/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

func TestGroupViewsAndExplicitUnixTime(t *testing.T) {
	c, db, _ := setup(t)
	ctx := context.Background()
	u := user.User{ID: "fixed", Username: "fixed", DisplayName: "Fixed", PasswordHash: "secret", Role: user.RoleUser,
		CreatedAt: time.Unix(1700000001, 0).UTC(), UpdatedAt: time.Unix(1700000002, 0).UTC()}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateGroup(ctx, user.Group{ID: "g-one", Name: "One", CreatedAt: u.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	if err := db.AddUserToGroup(ctx, u.ID, "g-one"); err != nil {
		t.Fatal(err)
	}
	listed, err := c.List(ctx, &api.ListReq{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Users) != 1 {
		t.Fatalf("list: %+v", listed)
	}
	view := listed.Users[0]
	if view.CreatedAt != 1700000001 || view.UpdatedAt != 1700000002 || len(view.GroupIDs) != 1 || view.GroupIDs[0] != "g-one" {
		t.Fatalf("view changed timestamps/groups: %+v", view)
	}
	updated, err := c.Update(ctx, &api.UpdateReq{UserID: u.ID, DisplayName: api.Optional[string]{Value: "Renamed", Set: true}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.User.DisplayName != "Renamed" || len(updated.User.GroupIDs) != 1 || updated.User.CreatedAt != 1700000001 || updated.User.UpdatedAt <= 1700000002 {
		t.Fatalf("updated view: %+v", updated)
	}
}

func TestPatchNullStringsAndValidationOrder(t *testing.T) {
	c, _, _ := setup(t)
	ctx := context.Background()
	u := create(t, c, "alice", "user")
	for _, tc := range []struct {
		body    string
		message string
	}{
		{`{"display_name":null}`, user.ValidateDisplayName("").Error()},
		{`{"role":null}`, roleError("")},
		{`{"display_name":"","role":"wrong"}`, user.ValidateDisplayName("").Error()},
	} {
		var req api.UpdateReq
		if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
			t.Fatal(err)
		}
		req.UserID = u.ID
		_, err := c.Update(ctx, &req)
		wantError(t, err, 400, "bad_request", tc.message)
	}
	_, err := c.ResetPassword(ctx, &api.ResetPasswordReq{UserID: "missing", Password: "password123"})
	wantError(t, err, 404, "not_found", "用户不存在")
}

func roleError(role string) string {
	_, err := user.ValidRole(role)
	return err.Error()
}

func TestStrictDecodedNullPreservesFrameworkPath(t *testing.T) {
	c, _, _ := setup(t)
	u := create(t, c, "alice", "user")
	var decoded api.UpdateReq
	if err := json.Unmarshal([]byte(`{"disabled":null}`), &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.UserID = "untrusted"
	ctx := httpwire.WithDecodedBody(context.Background(), &decoded)
	res, err := c.Update(ctx, &api.UpdateReq{UserID: u.ID})
	if err != nil || res.User.ID != u.ID || res.User.Disabled {
		t.Fatalf("strict null/path: %+v %v", res, err)
	}
	if decoded.UserID != "untrusted" {
		t.Fatal("controller mutated shared decoded request")
	}
}

func TestStrictDecodedCreateAndPassword(t *testing.T) {
	c, db, _ := setup(t)
	decoded := &api.CreateReq{Username: "strict", Password: "password123", Role: "user"}
	ctx := httpwire.WithDecodedBody(context.Background(), decoded)
	created, err := c.Create(ctx, &api.CreateReq{Username: "untrusted"})
	if err != nil || created.User.Username != "strict" {
		t.Fatalf("strict create: %+v %v", created, err)
	}
	password := &api.ResetPasswordReq{UserID: "untrusted", Password: "newpassword123"}
	ctx = httpwire.WithDecodedBody(context.Background(), password)
	_, err = c.ResetPassword(ctx, &api.ResetPasswordReq{UserID: created.User.ID, Password: ""})
	if !errors.Is(err, controller.ErrNoContent) {
		t.Fatal(err)
	}
	stored, err := db.GetUserByID(context.Background(), created.User.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := user.VerifyPassword(stored.PasswordHash, password.Password); err != nil {
		t.Fatal(err)
	}
	if password.UserID != "untrusted" || decoded.Username != "strict" {
		t.Fatal("decoded bodies mutated")
	}
}

func TestStoreFailureMapping(t *testing.T) {
	c, _, _ := setup(t)
	wantError(t, c.storeError(store.ErrNotFound, "用户不存在"), 404, "not_found", "用户不存在")
	wantError(t, c.storeError(store.ErrConflict, "ignored"), 409, "conflict", "名称已被占用，请换一个")
	wantError(t, c.storeError(context.Canceled, "ignored"), 500, "internal", "数据库操作失败")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	active := user.User{ID: "last", Role: user.RoleAdmin}
	disabled := true
	wantError(t, c.guardLastAdmin(ctx, active, store.UserUpdate{Disabled: &disabled}),
		500, "internal", "统计管理员数量失败")
}
