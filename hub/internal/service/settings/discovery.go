package settings

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"
)

type DiscoverySummary struct{ Issuer, AuthorizationEndpoint, TokenEndpoint, JWKSURI string }

func (s *Service) CallbackURL(id string) string { return s.base + "/api/auth/oidc/" + id + "/callback" }

// TestProvider never sends credentials and never disables TLS certificate checks.
func (s *Service) TestProvider(ctx context.Context, id string) (DiscoverySummary, error) {
	p, err := s.db.GetOIDCProvider(ctx, id)
	if err != nil {
		return DiscoverySummary{}, err
	}
	if err := s.validateIssuer(p.Issuer); err != nil {
		return DiscoverySummary{}, err
	}
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(bounded, http.MethodGet, strings.TrimRight(p.Issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return DiscoverySummary{}, invalid("登录服务地址无效")
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		return DiscoverySummary{}, &Error{Code: "bad_gateway", Message: "无法连接登录服务，请检查地址、网络与证书"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return DiscoverySummary{}, &Error{Code: "bad_gateway", Message: "登录服务未返回有效的发现文档"}
	}
	var doc struct {
		Issuer                string `json:"issuer"`
		AuthorizationEndpoint string `json:"authorization_endpoint"`
		TokenEndpoint         string `json:"token_endpoint"`
		JWKSURI               string `json:"jwks_uri"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(&doc); err != nil || doc.Issuer != p.Issuer {
		return DiscoverySummary{}, &Error{Code: "bad_gateway", Message: "登录服务发现文档的Issuer不匹配"}
	}
	for _, endpoint := range []string{doc.AuthorizationEndpoint, doc.TokenEndpoint, doc.JWKSURI} {
		if err := s.validateIssuer(endpoint); err != nil {
			return DiscoverySummary{}, &Error{Code: "bad_gateway", Message: "登录服务返回了不安全或缺失的端点"}
		}
	}
	return DiscoverySummary{Issuer: doc.Issuer, AuthorizationEndpoint: doc.AuthorizationEndpoint, TokenEndpoint: doc.TokenEndpoint, JWKSURI: doc.JWKSURI}, nil
}
