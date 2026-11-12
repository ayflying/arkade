// Package settings owns validated system policy and encrypted OIDC configuration.
package settings

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"net/url"
	"strings"

	"arkade/hub/internal/store"
	"github.com/gogf/gf/v2/errors/gerror"
)

type Options struct{ AllowInsecureLoopback bool }
type Service struct {
	db      *store.DB
	aead    cipher.AEAD
	base    string
	options Options
}
type ProviderCredentials struct {
	Provider                  store.OIDCProvider
	ClientSecret, RedirectURL string
	AllowInsecureLoopback     bool
}
type Error struct{ Code, Message string }

func (e *Error) Error() string     { return e.Message }
func invalid(message string) error { return &Error{Code: "bad_request", Message: message} }

func New(db *store.DB, key []byte, publicBaseURL string) (*Service, error) {
	return NewWithOptions(db, key, publicBaseURL, Options{})
}
func NewWithOptions(db *store.DB, key []byte, publicBaseURL string, options Options) (*Service, error) {
	if len(key) != 32 {
		return nil, gerror.New("settings encryption key must contain exactly 32 bytes")
	}
	u, err := url.Parse(publicBaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, gerror.New("invalid public base URL")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Service{db: db, aead: aead, base: strings.TrimRight(publicBaseURL, "/"), options: options}, nil
}
func (s *Service) encrypt(id, secret string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return "v1:" + base64.RawStdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(secret), []byte(id))), nil
}
func (s *Service) decrypt(id, value string) (string, error) {
	if !strings.HasPrefix(value, "v1:") {
		return "", gerror.New("invalid encrypted provider credential")
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(value, "v1:"))
	if err != nil || len(raw) < s.aead.NonceSize() {
		return "", gerror.New("invalid encrypted provider credential")
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], []byte(id))
	if err != nil {
		return "", gerror.New("provider credential cannot be decrypted")
	}
	return string(plain), nil
}
func (s *Service) Get(ctx context.Context) (store.SystemSettings, error) {
	return s.db.GetSystemSettings(ctx)
}
func (s *Service) Update(ctx context.Context, p store.SystemSettings, version int64) (store.SystemSettings, error) {
	if err := validateSettings(p); err != nil {
		return store.SystemSettings{}, err
	}
	if version <= 0 {
		return store.SystemSettings{}, invalid("必须提供有效设置版本")
	}
	if p.RegistrationGroupID != "" {
		if _, err := s.db.GetGroup(ctx, p.RegistrationGroupID); err != nil {
			return store.SystemSettings{}, invalid("默认注册用户组不存在")
		}
	}
	if err := s.db.UpdateSystemSettings(ctx, p, version); err != nil {
		return store.SystemSettings{}, err
	}
	return s.Get(ctx)
}
func validateSettings(p store.SystemSettings) error {
	if strings.TrimSpace(p.SiteName) == "" || len([]rune(p.SiteName)) > 100 || strings.ContainsAny(p.SiteName, "\r\n\x00") {
		return invalid("站点名称必须为1到100个字符且不含控制字符")
	}
	if len([]rune(p.LoginNotice)) > 4000 || strings.ContainsRune(p.LoginNotice, 0) {
		return invalid("登录提示不能超过4000个字符")
	}
	if p.SessionTTLSeconds < 3600 || p.SessionTTLSeconds > 30*86400 {
		return invalid("会话有效期必须在1小时到30天之间")
	}
	if p.PasswordMinLength < 8 || p.PasswordMinLength > 72 {
		return invalid("密码最小长度必须在8到72之间")
	}
	if p.LoginMaxAttempts < 3 || p.LoginMaxAttempts > 20 {
		return invalid("登录失败次数必须在3到20之间")
	}
	if p.LoginWindowSeconds < 60 || p.LoginWindowSeconds > 3600 {
		return invalid("登录限流窗口必须在60到3600秒之间")
	}
	if p.AuditRetentionDays < 1 || p.AuditRetentionDays > 3650 {
		return invalid("审计保留期必须在1到3650天之间")
	}
	return nil
}
