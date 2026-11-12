package store

import (
	"arkade/hub/internal/user"
	"context"
	"errors"
	"testing"
	"time"
)

func TestSettingsCASAndRegistrationTransaction(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	s, err := db.GetSystemSettings(ctx)
	if err != nil || s.Version != 1 || s.RegistrationEnabled {
		t.Fatal(s, err)
	}
	u := mkUser("new", "new", user.RoleAdmin)
	u.PasswordHash = "hash"
	if err := db.RegisterLocalUser(ctx, u, s.Version); !errors.Is(err, ErrRegistrationDisabled) {
		t.Fatal(err)
	}
	s.RegistrationEnabled = true
	if err := db.UpdateSystemSettings(ctx, s, s.Version); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateSystemSettings(ctx, s, s.Version); !errors.Is(err, ErrConflict) {
		t.Fatal("stale settings", err)
	}
	if err := db.RegisterLocalUser(ctx, u, 1); !errors.Is(err, ErrPasswordPolicyChanged) {
		t.Fatal(err)
	}
	if err := db.RegisterLocalUser(ctx, u, 2); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetUserByID(ctx, u.ID)
	if err != nil || got.Role != user.RoleUser || got.MustChangePassword {
		t.Fatal(got, err)
	}
}
func TestIdentityTransactionAndLastLoginProtection(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now()
	p := OIDCProvider{ID: "p", DisplayName: "provider", Issuer: "https://issuer", ClientID: "client", Enabled: true, AutoRegister: true, CreatedAt: now, UpdatedAt: now}
	if err := db.SaveOIDCProvider(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	u := mkUser("oidc", "oidc-random", user.RoleAdmin)
	u.PasswordHash = "untrusted"
	got, err := db.ResolveOIDCIdentity(ctx, "p", 1, "subject", u, "identity")
	if err != nil || got.PasswordHash != "" || got.Role != user.RoleUser {
		t.Fatal(got, err)
	}
	if err := db.CreateOIDCSession(ctx, "oidc-good", u.ID, 1, 4102444800, "p", 1, "subject"); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateOIDCSession(ctx, "oidc-stale", u.ID, 1, 2, "p", 2, "subject"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal("stale provider session gate", err)
	}
	if err := db.CreateOIDCSession(ctx, "oidc-unbound", u.ID, 1, 2, "p", 1, "other"); !errors.Is(err, ErrNotFound) {
		t.Fatal("binding session gate", err)
	}
	disabled := true
	if err := db.UpdateUser(ctx, u.ID, UserUpdate{Disabled: &disabled}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveOIDCIdentity(ctx, "p", 1, "subject", mkUser("replacement", "replacement", user.RoleUser), "replacement-id"); !errors.Is(err, ErrNotFound) {
		t.Fatal("disabled identity auto-created replacement", err)
	}
	if err := db.CreateUserSession(ctx, "disabled-token", u.ID, 1, 2, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal("disabled session gate", err)
	}
	disabled = false
	if err := db.UpdateUser(ctx, u.ID, UserUpdate{Disabled: &disabled}, 3); err != nil {
		t.Fatal(err)
	}
	if err := db.BindOIDCIdentity(ctx, "p", 1, Identity{ID: "duplicate", UserID: u.ID, Subject: "different", CreatedAt: now}); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate user/provider binding", err)
	}
	if err := db.DeleteIdentity(ctx, u.ID, "identity"); !errors.Is(err, ErrLastLoginMethod) {
		t.Fatal(err)
	}
	if err := db.DeleteOIDCProvider(ctx, "p"); !errors.Is(err, ErrLastLoginMethod) {
		t.Fatal(err)
	}
	p.Enabled = false
	if err := db.SaveOIDCProvider(ctx, p, 1); !errors.Is(err, ErrLastLoginMethod) {
		t.Fatal(err)
	}
	if _, err := db.ResolveOIDCIdentity(ctx, "p", 2, "subject", u, "other"); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
	if err := db.UpdateUserPassword(ctx, u.ID, "localhash", 1); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteOIDCProvider(ctx, "p"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetUserByID(ctx, u.ID); err != nil {
		t.Fatal("provider deletion removed user", err)
	}
	if identities, err := db.ListUserIdentities(ctx, u.ID); err != nil || len(identities) != 0 {
		t.Fatal(identities, err)
	}
}
func TestLastLocalAdminAndCredentialSessionCAS(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	u := mkUser("local", "local", user.RoleAdmin)
	u.MustChangePassword = true
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	oidc := mkUser("oidc", "oidc", user.RoleAdmin)
	oidc.PasswordHash = ""
	if err := db.CreateUser(ctx, oidc); err != nil {
		t.Fatal(err)
	}
	disabled := true
	if err := db.UpdateUser(ctx, u.ID, UserUpdate{Disabled: &disabled}, 2); !errors.Is(err, ErrLastLocalAdmin) {
		t.Fatal(err)
	}
	if err := db.DeleteUser(ctx, u.ID); !errors.Is(err, ErrLastLocalAdmin) {
		t.Fatal(err)
	}
	hash := "wrong"
	if err := db.CreateUserSession(ctx, "bad", u.ID, 1, 2, &hash); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	hash = u.PasswordHash
	if err := db.CreateUserSession(ctx, "good", u.ID, 1, 4102444800, &hash); err != nil {
		t.Fatal(err)
	}
	if err := db.ChangeLocalPassword(ctx, u.ID, "wrong", "changed", 2, false); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err := db.ChangeLocalPassword(ctx, u.ID, u.PasswordHash, "changed", 2, false); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetUserByID(ctx, u.ID)
	if err != nil || got.MustChangePassword || got.PasswordHash != "changed" {
		t.Fatal(got, err)
	}
	n, err := db.gdb.Model("sessions").Ctx(ctx).Count()
	if err != nil || n != 0 {
		t.Fatal("sessions not revoked", n, err)
	}
}
