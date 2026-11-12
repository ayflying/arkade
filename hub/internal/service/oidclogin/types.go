// Package oidclogin implements browser-bound OIDC authorization-code login.
package oidclogin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"github.com/gogf/gf/v2/errors/gerror"
	"net/http"
	"sync"
	"time"

	"arkade/hub/internal/auth"
	"arkade/hub/internal/store"
)

var (
	ErrFlow     = gerror.New("OIDC 登录流程无效或已过期，请重新发起")
	ErrProvider = gerror.New("身份提供方不可用，请联系管理员")
	ErrIdentity = gerror.New("无法使用该外部身份登录或绑定")
	ErrRedirect = gerror.New("登录后跳转地址无效")
)

// Credentials is a short-lived internal value; never serialize or log it.
type Credentials struct {
	Provider                  store.OIDCProvider
	ClientSecret, RedirectURL string
	AllowInsecureLoopback     bool
}
type ProviderLoader func(context.Context, string) (Credentials, error)
type Auth interface {
	IssueSession(context.Context, string) (auth.LoginResult, error)
	IssueOIDCSession(context.Context, string, string, int64, string) (auth.LoginResult, error)
	VerifyLocalPassword(context.Context, string, string) error
}
type AuditEvent struct{ Action, ProviderID, UserID, Outcome string }
type AuditHook func(context.Context, AuditEvent)
type Deps struct {
	Audit        AuditHook
	DB           *store.DB
	LoadProvider ProviderLoader
	Auth         Auth
	HTTPClient   *http.Client
	TTL          time.Duration
	MaxFlows     int
}
type Service struct {
	d     Deps
	mu    sync.Mutex
	flows map[string]flow
	now   func() time.Time
}
type flow struct {
	ProviderID                                      string
	Version                                         int64
	BrowserHash                                     [32]byte
	ActorID, SessionHash, Verifier, Nonce, Redirect string
	Expires                                         time.Time
}
type StartResult struct {
	URL, BrowserToken, State string
	Expires                  time.Time
}
type CompleteResult struct {
	Login    *auth.LoginResult
	Redirect string
	Bound    bool
}

func New(d Deps) *Service {
	if d.TTL <= 0 {
		d.TTL = 5 * time.Minute
	}
	if d.MaxFlows <= 0 {
		d.MaxFlows = 1024
	}
	if d.HTTPClient == nil {
		d.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	// Own a copy: callers cannot change redirect/timeout policy after construction.
	client := *d.HTTPClient
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = boundedTransport{base: transport}
	client.Timeout = 15 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	d.HTTPClient = &client
	return &Service{d: d, flows: map[string]flow{}, now: time.Now}
}
func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
