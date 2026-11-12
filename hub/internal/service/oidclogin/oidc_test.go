package oidclogin

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/auth"
	"arkade/hub/internal/store"
)

type fakeAuth struct{}

func (fakeAuth) IssueOIDCSession(context.Context, string, string, int64, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, nil
}

func (fakeAuth) IssueSession(context.Context, string) (auth.LoginResult, error) {
	return auth.LoginResult{}, nil
}
func (fakeAuth) VerifyLocalPassword(context.Context, string, string) error { return nil }

type fakeOIDC struct {
	server                                      *httptest.Server
	key                                         *rsa.PrivateKey
	nonce, challenge, issuer, audience, subject string
	expired                                     bool
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeOIDC{key: key, audience: "client", subject: "external-user"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": f.server.URL, "authorization_endpoint": f.server.URL + "/auth", "token_endpoint": f.server.URL + "/token", "jwks_uri": f.server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}, "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}})
		case "/jwks":
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "kid": "key", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
		case "/token":
			r.ParseForm()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != "code" || base64.RawURLEncoding.EncodeToString(hash[:]) != f.challenge {
				http.Error(w, "invalid code verifier", 400)
				return
			}
			issuer := f.server.URL
			if f.issuer != "" {
				issuer = f.issuer
			}
			expiry := time.Now().Add(time.Hour)
			if f.expired {
				expiry = time.Now().Add(-time.Hour)
			}
			claims := map[string]any{"iss": issuer, "aud": f.audience, "sub": f.subject, "exp": expiry.Unix(), "iat": time.Now().Unix(), "nonce": f.nonce}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "unused", "token_type": "Bearer", "id_token": f.sign(t, claims)})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}
func (f *fakeOIDC) sign(t *testing.T, claims any) string {
	t.Helper()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "key"})
	payload, _ := json.Marshal(claims)
	content := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(content))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return content + "." + base64.RawURLEncoding.EncodeToString(sig)
}
func fixture(t *testing.T) (*Service, *fakeOIDC, *Credentials) {
	f := newFakeOIDC(t)
	c := &Credentials{Provider: store.OIDCProvider{ID: "casdoor", Issuer: f.server.URL, ClientID: "client", Scopes: "openid profile", Enabled: true, Version: 1}, RedirectURL: "http://127.0.0.1/api/auth/oidc/casdoor/callback", AllowInsecureLoopback: true}
	s := New(Deps{Auth: fakeAuth{}, LoadProvider: func(context.Context, string) (Credentials, error) { return *c, nil }})
	return s, f, c
}
func begin(t *testing.T, s *Service, f *fakeOIDC) StartResult {
	t.Helper()
	r, err := s.Start(t.Context(), "casdoor", "/account")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(r.URL)
	q := u.Query()
	f.nonce = q.Get("nonce")
	f.challenge = q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || f.nonce == "" {
		t.Fatalf("missing PKCE/nonce: %s", r.URL)
	}
	return r
}
func TestUntrustedOIDCTokensRejected(t *testing.T) {
	for _, name := range []string{"nonce", "issuer", "audience", "expired", "signature"} {
		t.Run(name, func(t *testing.T) {
			s, f, _ := fixture(t)
			r := begin(t, s, f)
			switch name {
			case "nonce":
				f.nonce = "attacker"
			case "issuer":
				f.issuer = "https://attacker.invalid"
			case "audience":
				f.audience = "other-client"
			case "expired":
				f.expired = true
			case "signature":
				other, _ := rsa.GenerateKey(rand.Reader, 2048)
				f.key = other
			}
			_, err := s.Complete(t.Context(), "casdoor", r.State, r.BrowserToken, "code", "", "")
			if err == nil {
				t.Fatal("untrusted id token accepted")
			}
		})
	}
}
func TestStateBrowserReplayAndProviderChange(t *testing.T) {
	s, f, c := fixture(t)
	r := begin(t, s, f)
	if _, err := s.Complete(t.Context(), "casdoor", r.State, "wrong", "code", "", ""); err != ErrFlow {
		t.Fatal(err)
	}
	if _, err := s.Complete(t.Context(), "casdoor", r.State, r.BrowserToken, "code", "", ""); err != ErrFlow {
		t.Fatal("state replay accepted")
	}
	r = begin(t, s, f)
	c.Provider.Version++
	if _, err := s.Complete(t.Context(), "casdoor", r.State, r.BrowserToken, "code", "", ""); err != ErrProvider {
		t.Fatal(err)
	}
	r = begin(t, s, f)
	c.Provider.Enabled = false
	if _, err := s.Complete(t.Context(), "casdoor", r.State, r.BrowserToken, "code", "", ""); err != ErrProvider {
		t.Fatal(err)
	}
}
func TestRedirectAndIssuerValidation(t *testing.T) {
	for _, raw := range []string{"https://evil", "//evil", "/%2fevil", "/\\evil", "/%5cevil", "/a%0d%0aLocation:evil"} {
		if _, err := LocalRedirect(raw); err == nil {
			t.Fatalf("unsafe redirect %q", raw)
		}
	}
	for _, raw := range []string{"http://example.com", "https://user:pass@issuer", "https://issuer?secret=x", "https://issuer/#fragment"} {
		if validateURL(raw, true) {
			t.Fatal(raw)
		}
	}
	if !validateURL("http://127.0.0.1", true) || validateURL("http://127.0.0.1", false) {
		t.Fatal("loopback policy")
	}
	if safe, err := LocalRedirect("/account?tab=identities"); err != nil || !strings.HasPrefix(safe, "/account") {
		t.Fatal(err)
	}
}
