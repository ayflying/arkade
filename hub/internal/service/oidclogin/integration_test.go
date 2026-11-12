package oidclogin

import (
	"arkade/hub/internal/auth"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
	"arkade/hub/internal/user"
	"net/url"
	"testing"
	"time"
)

func integration(t *testing.T) (*Service, *fakeOIDC, *store.DB) {
	t.Helper()
	s, f, c := fixture(t)
	db, err := store.Open(t.Context(), testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	p := c.Provider
	p.AutoRegister = true
	p.CreatedAt = time.Now()
	p.UpdatedAt = p.CreatedAt
	if err := db.SaveOIDCProvider(t.Context(), p, 0); err != nil {
		t.Fatal(err)
	}
	s.d.DB = db
	s.d.Auth = auth.NewService(db, time.Hour)
	return s, f, db
}
func TestRealCodeJWKSLoginAndStableIdentity(t *testing.T) {
	s, f, db := integration(t)
	r := begin(t, s, f)
	out, err := s.Complete(t.Context(), "casdoor", r.State, r.BrowserToken, "code", "", "")
	if err != nil || out.Login == nil || out.Login.User.Role != user.RoleUser || out.Login.User.PasswordHash != "" || out.Redirect != "/account" {
		t.Fatalf("login %+v %v", out, err)
	}
	first := out.Login.User.ID
	r = begin(t, s, f)
	out, err = s.Complete(t.Context(), "casdoor", r.State, r.BrowserToken, "code", "", "")
	if err != nil || out.Login.User.ID != first {
		t.Fatalf("identity did not retain account: %+v %v", out, err)
	}
	list, err := db.ListUserIdentities(t.Context(), first)
	if err != nil || len(list) != 1 {
		t.Fatalf("identities=%+v %v", list, err)
	}
	disabled := true
	if err := db.UpdateUser(t.Context(), first, store.UserUpdate{Disabled: &disabled}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	r = begin(t, s, f)
	if _, err = s.Complete(t.Context(), "casdoor", r.State, r.BrowserToken, "code", "", ""); err == nil {
		t.Fatal("disabled identity silently created replacement user")
	}
}
func TestBindFlowRequiresSameActorAndSession(t *testing.T) {
	s, f, db := integration(t)
	now := time.Now()
	hash, hashErr := user.HashPassword("password")
	if hashErr != nil {
		t.Fatal(hashErr)
	}
	if err := db.CreateUser(t.Context(), user.User{ID: "local", Username: "local", Role: user.RoleUser, PasswordHash: hash, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	start, err := s.StartBind(t.Context(), "casdoor", "local", "original-session", "password", "/account")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete(t.Context(), "casdoor", start.State, start.BrowserToken, "code", "local", "different-session"); err != ErrFlow {
		t.Fatal("binding survived session change")
	}
	start, err = s.StartBind(t.Context(), "casdoor", "local", "original-session", "password", "/account")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(start.URL)
	f.nonce = u.Query().Get("nonce")
	f.challenge = u.Query().Get("code_challenge")
	out, err := s.Complete(t.Context(), "casdoor", start.State, start.BrowserToken, "code", "local", "original-session")
	if err != nil || !out.Bound || out.Login != nil {
		t.Fatalf("bind %+v %v", out, err)
	}
	identities, err := db.ListUserIdentities(t.Context(), "local")
	if err != nil || len(identities) != 1 {
		t.Fatalf("bind identities %+v %v", identities, err)
	}
}

func TestFlowExpiryAndCapacity(t *testing.T) {
	s, f, _ := fixture(t)
	s.d.MaxFlows = 1
	r := begin(t, s, f)
	if _, err := s.Start(t.Context(), "casdoor", "/"); err != ErrFlow {
		t.Fatal("flow limit not enforced")
	}
	s.now = func() time.Time { return time.Now().Add(10 * time.Minute) }
	if _, err := s.Complete(t.Context(), "casdoor", r.State, r.BrowserToken, "code", "", ""); err != ErrFlow {
		t.Fatal("expired flow accepted")
	}
}
