package oidclogin

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"

	oidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func validateURL(raw string, allowLoopback bool) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Hostname() == "" {
		return false
	}
	decoded, decodeErr := url.PathUnescape(raw)
	if decodeErr != nil {
		return false
	}
	for _, r := range decoded {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return false
		}
	}
	if u.Scheme == "https" {
		return true
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	return allowLoopback && u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback()))
}
func (s *Service) provider(ctx context.Context, id string) (Credentials, *oidc.Provider, oauth2.Config, error) {
	c, err := s.d.LoadProvider(ctx, id)
	if err != nil || !c.Provider.Enabled || !validateURL(c.Provider.Issuer, c.AllowInsecureLoopback) || !validateURL(c.RedirectURL, c.AllowInsecureLoopback) || c.Provider.ClientID == "" {
		return Credentials{}, nil, oauth2.Config{}, ErrProvider
	}
	ctx = oidc.ClientContext(ctx, s.d.HTTPClient)
	p, err := oidc.NewProvider(ctx, c.Provider.Issuer)
	if err != nil {
		return Credentials{}, nil, oauth2.Config{}, ErrProvider
	}
	endpoint := p.Endpoint()
	var claims struct {
		JWKS string `json:"jwks_uri"`
	}
	if p.Claims(&claims) != nil || !validateURL(endpoint.AuthURL, c.AllowInsecureLoopback) || !validateURL(endpoint.TokenURL, c.AllowInsecureLoopback) || !validateURL(claims.JWKS, c.AllowInsecureLoopback) {
		return Credentials{}, nil, oauth2.Config{}, ErrProvider
	}
	scopes := strings.Fields(c.Provider.Scopes)
	found := false
	for _, scope := range scopes {
		if scope == oidc.ScopeOpenID {
			found = true
		}
	}
	if !found {
		scopes = append([]string{oidc.ScopeOpenID}, scopes...)
	}
	cfg := oauth2.Config{ClientID: c.Provider.ClientID, ClientSecret: c.ClientSecret, RedirectURL: c.RedirectURL, Scopes: scopes, Endpoint: endpoint}
	return c, p, cfg, nil
}

// LocalRedirect rejects network paths, encoded separators, controls and backslashes.
func LocalRedirect(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	decoded, err := url.PathUnescape(raw)
	if err != nil || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.HasPrefix(decoded, "//") || strings.ContainsAny(raw, "\\\r\n\x00") || strings.ContainsAny(decoded, "\\\r\n\x00") {
		return "", ErrRedirect
	}
	for _, r := range decoded {
		if r < 0x20 || r == 0x7f {
			return "", ErrRedirect
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil {
		return "", ErrRedirect
	}
	return raw, nil
}
func clientContext(ctx context.Context, c *http.Client) context.Context {
	return oidc.ClientContext(ctx, c)
}
