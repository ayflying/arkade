package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
	"arkade/hub/internal/user"
)

func TestParseAPIKeyAuthorization(t *testing.T) {
	token := apiKeyPrefix + strings.Repeat("a", 32) + "." + strings.Repeat("A", 43)
	for _, tc := range []struct {
		name    string
		header  http.Header
		want    string
		present bool
	}{
		{"absent", nil, "", false},
		{"valid", http.Header{"Authorization": {"bEaReR  " + token}}, token, true},
		{"blank", http.Header{"Authorization": {""}}, "", true},
		{"multiple", http.Header{"Authorization": {"Bearer " + token, "Bearer " + token}}, "", true},
		{"alias", http.Header{"Authorization": {"Bearer " + token}, "authorization": {}}, "", true},
		{"combined", http.Header{"Authorization": {"Bearer " + token + ", Bearer " + token}}, "", true},
		{"wrong scheme", http.Header{"Authorization": {"Basic " + token}}, "", true},
		{"bad base64", http.Header{"Authorization": {"Bearer " + token[:len(token)-1] + "B"}}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, present := ParseAPIKeyAuthorization(tc.header)
			if got != tc.want || present != tc.present {
				t.Fatalf("unexpected parse: token valid=%v present=%v", got != "", present)
			}
		})
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	raw, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	s := NewService(db, time.Hour)
	for _, id := range []string{"owner", "other"} {
		u := user.User{ID: id, Username: id, DisplayName: id, Role: user.RoleAdmin, PasswordHash: "not-needed", CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := db.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	assertInvalid := func(token string) {
		t.Helper()
		if _, id, err := s.AuthenticateAPIKey(ctx, token); !errors.Is(err, ErrInvalidAPIKey) || id != "" {
			t.Fatalf("invalid token accepted: %v", err)
		}
	}
	create := func() (store.AdminAPIKey, string) {
		t.Helper()
		key, token, err := s.CreateAPIKey(ctx, "owner", "  测试  ")
		if err != nil {
			t.Fatal(err)
		}
		if key.Name != "测试" || !strings.HasPrefix(token, APIKeyDisplayPrefix(key.ID)) || len(token) < 50 {
			t.Fatal("unexpected token metadata")
		}
		u, id, err := s.AuthenticateAPIKey(ctx, token)
		if err != nil || id != key.ID || u.ID != "owner" || u.Role != user.RoleAdmin || u.PasswordHash != "" {
			t.Fatalf("identity invalid: id=%q err=%v", id, err)
		}
		return key, token
	}
	key, token := create()
	var digest string
	if err := raw.QueryRowContext(ctx, `SELECT token_hash FROM admin_api_keys WHERE id = $1`, key.ID).Scan(&digest); err != nil || digest != hashToken(token) {
		t.Fatalf("credential not stored as digest: %v", err)
	}
	if list, err := s.ListAPIKeys(ctx, "owner"); err != nil || len(list) != 1 || list[0] != key {
		t.Fatalf("unexpected owner list: %+v %v", list, err)
	}
	if list, err := s.ListAPIKeys(ctx, "other"); err != nil || len(list) != 0 {
		t.Fatalf("unexpected peer list: %+v %v", list, err)
	}
	if err := s.RevokeAPIKey(ctx, "other", key.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("cross-owner revoke: %v", err)
	}
	if err := s.RevokeAPIKey(ctx, "owner", key.ID); err != nil {
		t.Fatal(err)
	}
	assertInvalid(token)
	for _, update := range [][2]string{{"disabled=1", "disabled=0"}, {"role='user'", "role='admin'"}} {
		_, token = create()
		if _, err := raw.ExecContext(ctx, `UPDATE users SET `+update[0]+` WHERE id='owner'`); err != nil {
			t.Fatal(err)
		}
		assertInvalid(token)
		if _, _, err := s.CreateAPIKey(ctx, "owner", "forbidden"); !errors.Is(err, store.ErrAdminAPIKeyOwnerIneligible) {
			t.Fatalf("ineligible issuance: %v", err)
		}
		if _, err := raw.ExecContext(ctx, `UPDATE users SET `+update[1]+` WHERE id='owner'`); err != nil {
			t.Fatal(err)
		}
		assertInvalid(token)
	}
	_, token = create()
	if _, err := raw.ExecContext(ctx, `DELETE FROM users WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	assertInvalid(token)
	if list, err := s.ListAPIKeys(ctx, "owner"); err != nil || len(list) != 0 {
		t.Fatalf("owner deletion did not cascade: %+v %v", list, err)
	}
}
