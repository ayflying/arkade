package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"github.com/gogf/gf/v2/errors/gerror"
)

const apiKeyPrefix = "ark_mcp_"

var (
	ErrInvalidAPIKey     = gerror.New("MCP 密钥无效")
	ErrInvalidAPIKeyName = gerror.New("密钥名称须为 1–80 个字符，且不能包含控制字符")
)

// ParseAPIKeyAuthorization distinguishes an absent header from an invalid
// credential. Callers must not fall back to a session when a header is present.
func ParseAPIKeyAuthorization(header http.Header) (token string, present bool) {
	var values []string
	for k, v := range header {
		if strings.EqualFold(k, "Authorization") {
			if present {
				return "", true
			}
			values, present = v, true
		}
	}
	if !present || len(values) != 1 {
		return "", present
	}
	scheme, rest, ok := strings.Cut(strings.Trim(values[0], " \t"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	token = strings.TrimLeft(rest, " ")
	if !validAPIKey(token) {
		return "", true
	}
	return token, true
}

func validAPIKey(token string) bool {
	// 128-bit public ID, separate from the 256-bit random secret.
	if !strings.HasPrefix(token, apiKeyPrefix) || len(token) != len(apiKeyPrefix)+32+1+43 {
		return false
	}
	id, secret, ok := strings.Cut(token[len(apiKeyPrefix):], ".")
	if !ok || !validAPIKeyID(id) || len(secret) != 43 {
		return false
	}
	for _, c := range secret {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	_, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	return err == nil
}

func validAPIKeyID(id string) bool {
	if len(id) != 32 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func (s *Service) CreateAPIKey(ctx context.Context, userID, name string) (store.APIKey, string, error) {
	name = strings.TrimSpace(name)
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 80 || strings.ContainsFunc(name, unicode.IsControl) {
		return store.APIKey{}, "", ErrInvalidAPIKeyName
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return store.APIKey{}, "", gerror.Wrap(err, "生成 MCP 密钥 ID 失败")
	}
	secret, err := newToken()
	if err != nil {
		return store.APIKey{}, "", gerror.Wrap(err, "生成 MCP 密钥失败")
	}
	k := store.APIKey{ID: hex.EncodeToString(id[:]), Name: name, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	token := apiKeyPrefix + k.ID + "." + secret
	if err := s.db.InsertAPIKey(ctx, userID, k, hashToken(token)); err != nil {
		return store.APIKey{}, "", err
	}
	return k, token, nil
}

func (s *Service) ListAPIKeys(ctx context.Context, userID string) ([]store.APIKey, error) {
	return s.db.ListAPIKeys(ctx, userID)
}

func (s *Service) RevokeAPIKey(ctx context.Context, userID, id string) error {
	if !validAPIKeyID(id) {
		return store.ErrNotFound
	}
	return s.db.RevokeAPIKey(ctx, userID, id)
}

// APIKeyDisplayPrefix is derived solely from the public ID, not from the secret.
func APIKeyDisplayPrefix(id string) string {
	if !validAPIKeyID(id) {
		return apiKeyPrefix
	}
	return apiKeyPrefix + id[:8]
}

// AuthenticateAPIKey verifies the digest and the owner's current privileges in
// one query. Only the public key ID may be used for audit correlation.
func (s *Service) AuthenticateAPIKey(ctx context.Context, token string) (user.User, string, error) {
	if !validAPIKey(token) {
		return user.User{}, "", ErrInvalidAPIKey
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	keyID := token[len(apiKeyPrefix) : len(apiKeyPrefix)+32]
	u, err := s.db.AuthenticateAPIKey(ctx, keyID, hashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		return user.User{}, "", ErrInvalidAPIKey
	}
	if err != nil {
		return user.User{}, "", gerror.New("验证 MCP 密钥失败")
	}
	return u, keyID, nil
}
