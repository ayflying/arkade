package settings

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

func fixture(t *testing.T, options Options) (*Service, *store.DB) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	svc, err := NewWithOptions(db, []byte(strings.Repeat("k", 32)), "https://arkade.example", options)
	if err != nil {
		t.Fatal(err)
	}
	return svc, db
}
func secret(s string) *string { return &s }
func TestSettingsCASValidation(t *testing.T) {
	svc, _ := fixture(t, Options{})
	ctx := context.Background()
	initial, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	initial.SiteName = "Changed"
	initial.RegistrationEnabled = true
	initial.RegistrationGroupID = "" // Safe default: new users have no group/grants.
	updated, err := svc.Update(ctx, initial, initial.Version)
	if err != nil || updated.SiteName != "Changed" || updated.Version <= initial.Version {
		t.Fatal(updated, err)
	}
	if _, err := svc.Update(ctx, initial, initial.Version); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale CAS %v", err)
	}
	for _, mutate := range []func(*store.SystemSettings){func(p *store.SystemSettings) { p.SiteName = "" }, func(p *store.SystemSettings) { p.SessionTTLSeconds = 3599 }, func(p *store.SystemSettings) { p.SessionTTLSeconds = 30*86400 + 1 }, func(p *store.SystemSettings) { p.PasswordMinLength = 7 }, func(p *store.SystemSettings) { p.PasswordMinLength = 73 }, func(p *store.SystemSettings) { p.LoginMaxAttempts = 21 }, func(p *store.SystemSettings) { p.LoginWindowSeconds = 59 }, func(p *store.SystemSettings) { p.AuditRetentionDays = 3651 }} {
		bad := updated
		mutate(&bad)
		if _, err := svc.Update(ctx, bad, updated.Version); err == nil {
			t.Fatal("invalid policy accepted", bad)
		}
	}
}
func TestProviderEncryptionRetentionCASAndURLPolicy(t *testing.T) {
	svc, db := fixture(t, Options{})
	ctx := context.Background()
	input := ProviderInput{DisplayName: "Example", Issuer: "https://id.example", ClientID: "client", ClientSecret: secret("super-secret"), Enabled: true, AutoRegister: true}
	p, err := svc.SaveProvider(ctx, "", input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(p.ClientSecretCipher, "super-secret") || p.ClientSecretCipher == "" {
		t.Fatal("secret not encrypted")
	}
	loaded, err := svc.LoadProvider(ctx, p.ID)
	if err != nil || loaded.ClientSecret != "super-secret" || loaded.RedirectURL != "https://arkade.example/api/auth/oidc/"+p.ID+"/callback" {
		t.Fatal(loaded.RedirectURL, err)
	}
	if _, err := svc.decrypt("another-provider", p.ClientSecretCipher); err == nil {
		t.Fatal("ciphertext can be rebound")
	}
	input.ClientSecret = nil
	input.DisplayName = "Renamed"
	saved, err := svc.SaveProvider(ctx, p.ID, input, p.Version)
	if err != nil || saved.ClientSecretCipher != p.ClientSecretCipher {
		t.Fatal(saved, err)
	}
	if _, err := svc.SaveProvider(ctx, p.ID, input, p.Version); !errors.Is(err, store.ErrConflict) {
		t.Fatal("stale provider accepted", err)
	}
	input.ClientSecret = secret("")
	if _, err := svc.SaveProvider(ctx, p.ID, input, saved.Version); err == nil {
		t.Fatal("empty secret accepted")
	}
	input.ClientSecret = secret("replacement")
	saved, err = svc.SaveProvider(ctx, p.ID, input, saved.Version)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = svc.LoadProvider(ctx, p.ID)
	if err != nil || loaded.ClientSecret != "replacement" {
		t.Fatal(err)
	}
	raw, err := db.GetOIDCProvider(ctx, p.ID)
	if err != nil || raw.ClientSecretCipher == "replacement" {
		t.Fatal("plaintext stored", err)
	}
	for _, issuer := range []string{"http://id.example", "https://u:p@id.example", "https://id.example?x=1", "https://id.example#x", "http://127.0.0.1"} {
		if err := svc.validateIssuer(issuer); err == nil {
			t.Fatal("unsafe issuer", issuer)
		}
	}
	dev, _ := fixture(t, Options{AllowInsecureLoopback: true})
	if err := dev.validateIssuer("http://127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	if err := dev.validateIssuer("http://192.168.1.2"); err == nil {
		t.Fatal("non-loopback HTTP allowed")
	}
	if _, err := New(db, []byte("short"), "https://arkade.example"); err == nil {
		t.Fatal("short key allowed")
	}
}
func TestProviderExactIssuerAndBoundConfiguration(t *testing.T) {
	svc, db := fixture(t, Options{})
	ctx := context.Background()
	input := ProviderInput{DisplayName: "exact", Issuer: "https://id.example/tenant/", ClientID: "client", ClientSecret: secret("secret"), Enabled: true}
	p, err := svc.SaveProvider(ctx, "", input, 0)
	if err != nil {
		t.Fatal(err)
	}
	if p.Issuer != input.Issuer {
		t.Fatalf("issuer changed %q", p.Issuer)
	}
	if err := db.CreateUser(ctx, user.User{ID: "bound", Username: "bound-user", Role: user.RoleUser}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateIdentity(ctx, store.Identity{ID: "identity", UserID: "bound", ProviderID: p.ID, Subject: "subject"}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ProviderInput){func(v *ProviderInput) { v.Issuer = "https://other.example" }, func(v *ProviderInput) { v.Issuer = "https://id.example/tenant" }, func(v *ProviderInput) { v.ClientID = "other-client" }} {
		modified := input
		change(&modified)
		_, err := svc.SaveProvider(ctx, p.ID, modified, p.Version)
		var domain *Error
		if !errors.As(err, &domain) || domain.Code != "conflict" || !strings.Contains(domain.Message, "存在绑定身份") {
			t.Fatalf("bound provider modification: %v", err)
		}
	}
	input.ClientSecret = secret("rotated")
	if _, err := svc.SaveProvider(ctx, p.ID, input, p.Version); err != nil {
		t.Fatalf("bound secret rotation blocked: %v", err)
	}
}

func TestProviderDiscoverySafeSummary(t *testing.T) {
	svc, _ := fixture(t, Options{AllowInsecureLoopback: true})
	ctx := context.Background()
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("discovery sent credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issuer":"` + issuer + `","authorization_endpoint":"` + issuer + `/auth","token_endpoint":"` + issuer + `/token","jwks_uri":"` + issuer + `/keys"}`))
	}))
	defer server.Close()
	issuer = server.URL
	p, err := svc.SaveProvider(ctx, "", ProviderInput{DisplayName: "local", Issuer: issuer, ClientID: "id", ClientSecret: secret("secret")}, 0)
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.TestProvider(ctx, p.ID)
	if err != nil || out.Issuer != issuer {
		t.Fatal(out, err)
	}
}
