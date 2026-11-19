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
	for id, role := range map[string]user.Role{"owner": user.RoleUser, "other": user.RoleAdmin} {
		u := user.User{ID: id, Username: id, DisplayName: id, Role: role, PasswordHash: "not-needed", CreatedAt: time.Now(), UpdatedAt: time.Now()}
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
	assertActor := func(key store.APIKey, token string, role user.Role) {
		t.Helper()
		u, id, err := s.AuthenticateAPIKey(ctx, token)
		if err != nil || id != key.ID || u.ID != "owner" || u.Role != role || u.PasswordHash != "" {
			t.Fatalf("identity invalid: id=%q role=%q err=%v", id, u.Role, err)
		}
	}
	create := func(role user.Role) (store.APIKey, string) {
		t.Helper()
		key, token, err := s.CreateAPIKey(ctx, "owner", "  测试  ")
		if err != nil {
			t.Fatal(err)
		}
		if key.Name != "测试" || !strings.HasPrefix(token, APIKeyDisplayPrefix(key.ID)) || len(token) < 50 {
			t.Fatal("unexpected token metadata")
		}
		assertActor(key, token, role)
		return key, token
	}
	update := func(set string) {
		t.Helper()
		if _, err := raw.ExecContext(ctx, `UPDATE users SET `+set+` WHERE id='owner'`); err != nil {
			t.Fatal(err)
		}
	}
	assertCannotIssue := func() {
		t.Helper()
		if _, _, err := s.CreateAPIKey(ctx, "owner", "forbidden"); !errors.Is(err, store.ErrAPIKeyOwnerIneligible) {
			t.Fatalf("ineligible issuance: %v", err)
		}
	}
	key, token := create(user.RoleUser)
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
	key, token = create(user.RoleUser)
	update("role='admin'")
	assertActor(key, token, user.RoleAdmin)
	update("role='user'")
	assertInvalid(token)
	_, token = create(user.RoleUser)
	update("must_change_password=1")
	assertInvalid(token)
	assertCannotIssue()
	update("must_change_password=0")
	update("disabled=1")
	assertInvalid(token)
	assertCannotIssue()
	update("disabled=0")
	assertInvalid(token)
	_, token = create(user.RoleUser)
	if _, err := raw.ExecContext(ctx, `DELETE FROM users WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	assertInvalid(token)
	if list, err := s.ListAPIKeys(ctx, "owner"); err != nil || len(list) != 0 {
		t.Fatalf("owner deletion did not cascade: %+v %v", list, err)
	}
}
