package store

import (
	"arkade/hub/internal/user"
	"context"
	"errors"
	"testing"
	"time"
)

func TestBoundProviderAuthorityCannotChange(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	now := time.Now()
	p := OIDCProvider{ID: "authority", Issuer: "https://issuer-a.example/", ClientID: "client-a", Enabled: true, AutoRegister: true, CreatedAt: now, UpdatedAt: now}
	if err := db.SaveOIDCProvider(ctx, p, 0); err != nil {
		t.Fatal(err)
	}
	original := mkUser("issuer-a-user", "issuer-a-user", user.RoleUser)
	if _, err := db.ResolveOIDCIdentity(ctx, p.ID, 1, "shared-subject", original, "issuer-a-identity"); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ issuer, client string }{
		{"https://issuer-b.example/", "client-a"},
		{"https://issuer-a.example", "client-a"}, // Exact issuer: trailing slash is significant.
		{"https://issuer-a.example/", "client-b"},
	} {
		changed := p
		changed.Issuer = mutation.issuer
		changed.ClientID = mutation.client
		if err := db.SaveOIDCProvider(ctx, changed, 1); !errors.Is(err, ErrConflict) || !errors.Is(err, ErrProviderIdentityLocked) {
			t.Fatalf("bound identity authority change allowed: %v", err)
		}
	}
	got, err := db.GetOIDCProvider(ctx, p.ID)
	if err != nil || got.Version != 1 || got.Issuer != p.Issuer || got.ClientID != p.ClientID {
		t.Fatal("rejected change altered provider", err)
	}
	// Secret rotation is permitted without changing the subject authority.
	p.ClientSecretCipher = "rotated-cipher"
	if err := db.SaveOIDCProvider(ctx, p, 1); err != nil {
		t.Fatal(err)
	}
	resolved, err := db.ResolveOIDCIdentity(ctx, p.ID, 2, "shared-subject", mkUser("attacker", "attacker", user.RoleUser), "other-id")
	if err != nil || resolved.ID != original.ID {
		t.Fatal("original binding changed", err)
	}
	// A separate issuer with the same sub is an independent account, not the old user.
	second := p
	second.ID = "authority-b"
	second.Issuer = "https://issuer-b.example/"
	if err := db.SaveOIDCProvider(ctx, second, 0); err != nil {
		t.Fatal(err)
	}
	newcomer := mkUser("issuer-b-user", "issuer-b-user", user.RoleUser)
	resolved, err = db.ResolveOIDCIdentity(ctx, second.ID, 1, "shared-subject", newcomer, "issuer-b-identity")
	if err != nil || resolved.ID == original.ID || resolved.ID != newcomer.ID {
		t.Fatal("cross-issuer subject takeover", err)
	}
}
