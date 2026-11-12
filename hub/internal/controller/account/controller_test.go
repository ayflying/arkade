package account

import (
	api "arkade/hub/api/account/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type sessionStub struct {
	u  user.User
	ok bool
}

func (s *sessionStub) CurrentUser(context.Context) (user.User, bool) { return s.u, s.ok }
func (*sessionStub) Token(context.Context) string                    { return "" }
func (*sessionStub) SetCookie(context.Context, string, time.Time)    {}
func (*sessionStub) ClearCookie(context.Context)                     {}
func TestGetFreshUserAndNoHashInView(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "account.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u := user.User{ID: "u", Username: "temp", DisplayName: "Current", Role: user.RoleUser, PasswordHash: "secret-hash", MustChangePassword: true}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	session := &sessionStub{u: user.User{ID: "u", DisplayName: "Stale"}, ok: true}
	c := New(db, session, slog.New(slog.NewTextHandler(io.Discard, nil)))
	res, err := c.Get(ctx, &api.GetReq{})
	if err != nil {
		t.Fatal(err)
	}
	if res.User.DisplayName != "Current" || !res.User.MustChangePassword || !res.User.LocalPassword {
		t.Fatal(res)
	}
	data, _ := json.Marshal(res)
	if strings.Contains(string(data), "secret-hash") || strings.Contains(string(data), `"password_hash"`) {
		t.Fatal("hash leaked")
	}
	session.ok = false
	if _, err := c.Get(ctx, &api.GetReq{}); !errors.Is(err, controller.ErrUnauthorized) {
		t.Fatal(err)
	}
}
func TestValidationFailureIsClientError(t *testing.T) {
	c := &Controller{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	err := c.failure(store.ErrRegistrationDisabled)
	var failure *controller.Error
	if !errors.As(err, &failure) || failure.Status != 403 {
		t.Fatal(err)
	}
}
