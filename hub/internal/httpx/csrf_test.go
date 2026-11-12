package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserMutationOriginBoundary(t *testing.T) {
	cases := []struct {
		name, method, path, origin, fetch string
		want                              bool
	}{
		{"same origin", http.MethodPut, "/api/settings", "https://arkade.example", "same-origin", true},
		{"CLI", http.MethodPut, "/api/settings", "", "", true},
		{"other host", http.MethodPost, "/api/account/identities/oidc/bind", "https://evil.example", "", false},
		{"same site sibling", http.MethodDelete, "/api/account/identities/identity", "https://other.example", "same-site", false},
		{"null origin", http.MethodPost, "/api/auth/register", "null", "", false},
		{"forged path", http.MethodPost, "/api/auth/login", "https://arkade.example/path", "", false},
		{"cross fetch", http.MethodPut, "/api/settings", "", "cross-site", false},
		{"OIDC callback", http.MethodGet, "/api/auth/oidc/p/callback", "https://issuer.example", "cross-site", true},
		{"agent handshake", http.MethodGet, "/api/agent/connect", "https://other.example", "cross-site", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "https://arkade.example"+tc.path, nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.fetch)
			if got := browserMutationAllowed(r); got != tc.want {
				t.Fatalf("allowed=%v want=%v", got, tc.want)
			}
		})
	}
}
func TestPasswordChangeGateAllowlist(t *testing.T) {
	for _, path := range []string{"/api/auth/me", "/api/auth/logout", "/api/account", "/api/account/password"} {
		if !passwordChangeAllowed(path) {
			t.Fatalf("blocked recovery: %s", path)
		}
	}
	for _, path := range []string{"/api/settings", "/api/agents", "/api/account/identities", "/api/account/password/../identities", "/api/account/password/"} {
		if passwordChangeAllowed(path) {
			t.Fatalf("allowed resource before password change: %s", path)
		}
	}
}
