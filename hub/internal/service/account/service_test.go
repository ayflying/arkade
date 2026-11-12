package account

import (
	"arkade/hub/internal/auth"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Service, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "account.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return New(db), db
}
func TestRegistrationDisabledThenOrdinaryUser(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	if _, err := s.Register(ctx, "registered", "Registered", "Password12345!"); !errors.Is(err, store.ErrRegistrationDisabled) {
		t.Fatal(err)
	}
	settings, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.RegistrationEnabled = true
	if err := db.UpdateSystemSettings(ctx, settings, settings.Version); err != nil {
		t.Fatal(err)
	}
	u, err := s.Register(ctx, " REGISTERED ", "Registered", "Password12345!")
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "registered" || u.Role != user.RoleUser || u.Disabled || u.MustChangePassword {
		t.Fatal(u)
	}
	if _, err := s.Register(ctx, "registered", "Other", "Password12345!"); !errors.Is(err, store.ErrConflict) {
		t.Fatal(err)
	}
}
func TestChangePasswordRevokesAllSessionsAndClearsTemporaryFlag(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	hash, err := user.HashPassword("Temporary12345!")
	if err != nil {
		t.Fatal(err)
	}
	u := user.User{ID: "u", Username: "temp", DisplayName: "Temp", Role: user.RoleUser, PasswordHash: hash, MustChangePassword: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	a := auth.NewService(db, time.Hour)
	login, err := a.Login(ctx, "temp", "Temporary12345!")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, "u", "wrong", "UpdatedPassword123!"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, "u", "Temporary12345!", "UpdatedPassword123!"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Authenticate(ctx, login.Token); !errors.Is(err, auth.ErrSessionExpired) {
		t.Fatal("old session survived", err)
	}
	changed, err := db.GetUserByID(ctx, "u")
	if err != nil || changed.MustChangePassword || user.VerifyPassword(changed.PasswordHash, "UpdatedPassword123!") != nil {
		t.Fatal(changed, err)
	}
}
func TestDynamicPasswordMinimumAndOIDCOnlyDenial(t *testing.T) {
	s, db := fixture(t)
	ctx := context.Background()
	settings, err := db.GetSystemSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.RegistrationEnabled = true
	settings.PasswordMinLength = 20
	if err := db.UpdateSystemSettings(ctx, settings, settings.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Register(ctx, "newuser", "New", "Password12345!"); err == nil {
		t.Fatal("dynamic minimum ignored")
	}
	if err := db.CreateUser(ctx, user.User{ID: "oidc", Username: "oidc", DisplayName: "OIDC", Role: user.RoleUser}); err != nil {
		t.Fatal(err)
	}
	if err := s.ChangePassword(ctx, "oidc", "", "UpdatedPassword123!"); !errors.Is(err, ErrNoLocalPassword) {
		t.Fatal(err)
	}
}
