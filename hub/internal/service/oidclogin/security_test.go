package oidclogin

import (
	"arkade/hub/internal/store"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoveryRejectsUnsafeEndpointsBeforeExchange(t *testing.T) {
	for _, field := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri"} {
		for _, bad := range []string{"http://192.168.1.1/private", "https://user:pass@example.test/path", "https://example.test/path?secret=x", "https://example.test/path#fragment", "https://example.test/path?", "https://example.test/%0aheader"} {
			t.Run(field+bad, func(t *testing.T) {
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					payload := map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/auth", "token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks"}
					payload[field] = bad
					json.NewEncoder(w).Encode(payload)
				}))
				defer server.Close()
				s := New(Deps{LoadProvider: func(context.Context, string) (Credentials, error) {
					return Credentials{Provider: store.OIDCProvider{ID: "p", Issuer: server.URL, ClientID: "client", Enabled: true}, RedirectURL: server.URL + "/callback", AllowInsecureLoopback: true}, nil
				}})
				if _, err := s.Start(t.Context(), "p", "/"); err != ErrProvider {
					t.Fatalf("unsafe discovery accepted %s %v", bad, err)
				}
			})
		}
	}
}
func TestDiscoveryRequiresExactIssuerIncludingTrailingSlash(t *testing.T) {
	var issuer string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "auth", "token_endpoint": issuer + "token", "jwks_uri": issuer + "jwks"})
	}))
	defer server.Close()
	issuer = server.URL + "/"
	s := New(Deps{LoadProvider: func(context.Context, string) (Credentials, error) {
		return Credentials{Provider: store.OIDCProvider{ID: "p", Issuer: server.URL + "/", ClientID: "client", Enabled: true}, RedirectURL: server.URL + "/callback", AllowInsecureLoopback: true}, nil
	}})
	if _, err := s.Start(t.Context(), "p", "/"); err != nil {
		t.Fatalf("valid trailing-slash issuer rejected: %v", err)
	}
	issuer = strings.TrimSuffix(issuer, "/")
	if _, err := s.Start(t.Context(), "p", "/"); err != ErrProvider {
		t.Fatalf("issuer equality incorrectly normalized: %v", err)
	}
}
func TestURLBoundaryControls(t *testing.T) {
	for _, value := range []string{"https://issuer?", "https://issuer/%00", "https://issuer/%5c", "https:///path", "https://issuer/%7f", "https://issuer/\\path"} {
		if validateURL(value, false) {
			t.Fatalf("unsafe URL %q", value)
		}
	}
	for _, value := range []string{"/\t/evil", "/%09/evil", "/%7f/evil", "/\\evil", "/%2f%2fevil", "//user@evil", "/%0b/evil"} {
		if _, err := LocalRedirect(value); err == nil {
			t.Fatalf("unsafe redirect %q", value)
		}
	}
}
