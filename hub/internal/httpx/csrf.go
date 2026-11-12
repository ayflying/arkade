package httpx

import (
	"net/http"
	"net/url"
	"strings"
)

// Reject cross-origin browser mutations before any handler can change account
// policy or bind an identity. Requests without browser origin metadata remain
// available to existing CLI clients; JSON-only bodies and Lax cookies provide
// the complementary boundary against legacy form submissions.
func browserMutationAllowed(r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		return true
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	scheme := "http"
	if isHTTPS(r) {
		scheme = "https"
	}
	return parsed.Scheme == scheme && strings.EqualFold(parsed.Host, r.Host)
}
