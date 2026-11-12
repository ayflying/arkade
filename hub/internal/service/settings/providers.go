package settings

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"arkade/hub/internal/store"
)

type ProviderInput struct {
	DisplayName, Issuer, ClientID, Scopes, DefaultGroupID string
	Enabled, AutoRegister                                 bool
	ClientSecret                                          *string
}

func (s *Service) ListProviders(ctx context.Context) ([]store.OIDCProvider, error) {
	return s.db.ListOIDCProviders(ctx)
}
func (s *Service) GetProvider(ctx context.Context, id string) (store.OIDCProvider, error) {
	return s.db.GetOIDCProvider(ctx, id)
}
func (s *Service) LoadProvider(ctx context.Context, id string) (ProviderCredentials, error) {
	p, err := s.db.GetOIDCProvider(ctx, id)
	if err != nil {
		return ProviderCredentials{}, err
	}
	if !p.Enabled {
		return ProviderCredentials{}, &Error{Code: "not_found", Message: "登录服务不可用"}
	}
	if err := s.validateIssuer(p.Issuer); err != nil {
		return ProviderCredentials{}, err
	}
	secret, err := s.decrypt(p.ID, p.ClientSecretCipher)
	if err != nil {
		return ProviderCredentials{}, err
	}
	return ProviderCredentials{Provider: p, ClientSecret: secret, RedirectURL: s.base + "/api/auth/oidc/" + url.PathEscape(p.ID) + "/callback", AllowInsecureLoopback: s.options.AllowInsecureLoopback}, nil
}
func (s *Service) SaveProvider(ctx context.Context, id string, input ProviderInput, version int64) (store.OIDCProvider, error) {
	var p store.OIDCProvider
	if id == "" {
		if version != 0 {
			return p, invalid("创建登录服务不接受已有版本")
		}
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return p, err
		}
		id = "oidc-" + hex.EncodeToString(b[:])
		p.ID = id
		p.CreatedAt = time.Now().UTC()
	} else {
		if version <= 0 {
			return p, invalid("必须提供登录服务版本")
		}
		existing, err := s.db.GetOIDCProvider(ctx, id)
		if err != nil {
			return p, err
		}
		p = existing
	}
	p.DisplayName = strings.TrimSpace(input.DisplayName)
	p.Issuer = strings.TrimSpace(input.Issuer)
	p.ClientID = strings.TrimSpace(input.ClientID)
	p.Enabled = input.Enabled
	p.AutoRegister = input.AutoRegister
	p.DefaultGroupID = input.DefaultGroupID
	scopes := strings.Fields(input.Scopes)
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}
	found := false
	for _, v := range scopes {
		if v == "openid" {
			found = true
		}
	}
	if !found {
		scopes = append([]string{"openid"}, scopes...)
	}
	p.Scopes = strings.Join(scopes, " ")
	if p.DisplayName == "" || len([]rune(p.DisplayName)) > 100 || strings.ContainsAny(p.DisplayName, "\r\n\x00") {
		return p, invalid("登录服务名称必须为1到100个字符")
	}
	if err := s.validateIssuer(p.Issuer); err != nil {
		return p, err
	}
	if p.ClientID == "" || len(p.ClientID) > 1024 || strings.ContainsAny(p.ClientID, "\r\n\x00") {
		return p, invalid("请提供有效客户端ID")
	}
	if len(p.Scopes) > 2048 || strings.ContainsAny(p.Scopes, "\r\n\x00") {
		return p, invalid("登录授权范围无效")
	}
	if input.ClientSecret != nil {
		if *input.ClientSecret == "" || len(*input.ClientSecret) > 16384 {
			return p, invalid("客户端密钥不能为空或超过16384字节")
		}
		encrypted, err := s.encrypt(id, *input.ClientSecret)
		if err != nil {
			return p, err
		}
		p.ClientSecretCipher = encrypted
	}
	if p.Enabled {
		if p.ClientSecretCipher == "" {
			return p, invalid("启用登录服务前必须设置客户端密钥")
		}
		if _, err := s.decrypt(id, p.ClientSecretCipher); err != nil {
			return p, invalid("客户端密钥不可用，请重新设置")
		}
	}
	if p.DefaultGroupID != "" {
		if _, err := s.db.GetGroup(ctx, p.DefaultGroupID); err != nil {
			return p, invalid("登录服务默认用户组不存在")
		}
	}
	if err := s.db.SaveOIDCProvider(ctx, p, version); err != nil {
		if errors.Is(err, store.ErrProviderIdentityLocked) {
			return p, &Error{Code: "conflict", Message: "存在绑定身份，不可更改Issuer或客户端ID，请新建登录提供商"}
		}
		return p, err
	}
	return s.db.GetOIDCProvider(ctx, id)
}
func (s *Service) DeleteProvider(ctx context.Context, id string) error {
	return s.db.DeleteOIDCProvider(ctx, id)
}
func (s *Service) validateIssuer(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return invalid("Issuer必须是无用户名、查询参数和片段的HTTPS地址")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if s.options.AllowInsecureLoopback && u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return invalid("Issuer必须使用HTTPS；仅显式开发配置允许本机HTTP")
}
