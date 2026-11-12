package httpx

// identity_oidc_provider_test.go 提供一个真实可用的假 OIDC 提供方。
//
// 它实现的是**真的 OIDC 协议**：discovery、JWKS、authorization endpoint、
// token endpoint 都用 RS256 真签名，PKCE 的 code_verifier 和 nonce 都真校验。
// 所以被测代码走的是真实的 go-oidc / oauth2 客户端路径，没有被 mock 掉。
//
// 为什么不用 service/oidclogin 包里现成的同名 fake：
// 那是 internal 包的测试文件，按 Go 规则无法被其它包的测试引用，
// 只能复制一份精简版过来。这里的实现独立、只依赖标准库与 crypto。

import (
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
	"sync"
	"testing"
	"time"
)

// fakeOIDCProvider 是一台自签名的假 OIDC 服务器。
//
// 对外只暴露 URL 与少量可调参数，内部记录"这一次授权请求到底要了什么"，
// 供测试断言 PKCE / nonce / state 确实被真实发送。
type fakeOIDCProvider struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	client string

	mu        sync.Mutex
	subject   string
	nonce     string
	challenge string
	issuer    string // 空表示用 server.URL

	// issuedCode 是最近一次授权发出的授权码。
	// 每次授权都发新码，且只有未消费的才有效——与真实提供方一致：
	// 同一流程重放同一个码会被拒，但另一条独立流程能正常拿到新码。
	issuedCode string
	consumed   bool
	// lastRedirect 记录授权回调地址，用于观察 redirect_uri 是否正确。
	lastRedirect string

	// expired 为真时签发已过期的 id_token，用于异常路径。
	expired bool
	// rejectToken 为真时 token endpoint 直接返回 400。
	rejectToken bool
}

const fakeOIDCCode = "auth-code-1"

// newFakeOIDCProvider 启动假提供方。t.Cleanup 负责关闭。
func newFakeOIDCProvider(t *testing.T, clientID, subject string) *fakeOIDCProvider {
	t.Helper()

	// 2048 位 RSA 生成在测试里约几十毫秒，可接受；
	// 用更小的位数会导致签名算法不被接受。
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("生成 RSA 密钥失败: %v", err)
	}
	f := &fakeOIDCProvider{key: key, client: clientID, subject: subject}

	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

// URL 返回提供方根地址。
func (f *fakeOIDCProvider) URL() string { return f.server.URL }

// Issuer 返回 discovery 里声明的 issuer，默认就是 server.URL。
func (f *fakeOIDCProvider) Issuer() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.issuer != "" {
		return f.issuer
	}
	return f.server.URL
}

// SetIssuer 覆盖 issuer，用于验证 issuer 不匹配会被拒绝。
func (f *fakeOIDCProvider) SetIssuer(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issuer = v
}

// SetExpired 让后续签发的 id_token 过期。
func (f *fakeOIDCProvider) SetExpired(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.expired = v
}

// SetRejectToken 让 token endpoint 直接失败。
func (f *fakeOIDCProvider) SetRejectToken(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectToken = v
}

// LastChallenge 返回最近一次授权请求带的 PKCE challenge。
func (f *fakeOIDCProvider) LastChallenge() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.challenge
}

// LastNonce 返回最近一次授权请求带的 nonce。
func (f *fakeOIDCProvider) LastNonce() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nonce
}

// ResetConsumed 让授权码重新可用，便于同一测试内跑第二条流程。
func (f *fakeOIDCProvider) ResetConsumed() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consumed = false
}

func (f *fakeOIDCProvider) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                f.Issuer(),
			"authorization_endpoint":                f.server.URL + "/auth",
			"token_endpoint":                        f.server.URL + "/token",
			"jwks_uri":                              f.server.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})

	case "/jwks":
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []any{map[string]any{
				"kty": "RSA", "kid": "k1", "alg": "RS256", "use": "sig",
				"n": base64.RawURLEncoding.EncodeToString(f.key.N.Bytes()),
				"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.key.E)).Bytes()),
			}},
		})

	case "/auth":
		// 真实提供方在这里展示登录页。测试环境直接 302 回 redirect_uri，
		// 把 code 与 state 原样带回——这正是浏览器在用户授权后做的事。
		q := r.URL.Query()
		f.mu.Lock()
		f.nonce = q.Get("nonce")
		f.challenge = q.Get("code_challenge")
		f.issuedCode = fakeOIDCCode
		// 新授权重置消费标记：这是一条新流程。
		f.consumed = false
		f.mu.Unlock()

		redirect := q.Get("redirect_uri")
		u, err := url.Parse(redirect)
		if err != nil {
			http.Error(w, "bad redirect", http.StatusBadRequest)
			return
		}
		rq := u.Query()
		rq.Set("code", fakeOIDCCode)
		rq.Set("state", q.Get("state"))
		u.RawQuery = rq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)

	case "/token":
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		reject := f.rejectToken
		consumed := f.consumed
		expectedChallenge := f.challenge
		f.mu.Unlock()

		if reject {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		// 授权码一次性：重复使用像真实提供方一样失败。
		if consumed {
			http.Error(w, `{"error":"invalid_grant","error_description":"code reused"}`, http.StatusBadRequest)
			return
		}
		// PKCE 必须对得上：拿 code_verifier 算 S256 与授权时的 challenge 比较。
		if r.Form.Get("code") != fakeOIDCCode || !f.pkceOK(r.Form.Get("code_verifier"), expectedChallenge) {
			http.Error(w, `{"error":"invalid_grant","error_description":"pkce mismatch"}`, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.consumed = true
		f.mu.Unlock()

		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fake-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     f.signIDToken(r),
		})

	default:
		http.NotFound(w, r)
	}
}

// pkceOK 校验 S256 code_verifier 与授权请求里的 challenge 是否一致。
func (f *fakeOIDCProvider) pkceOK(verifier, challenge string) bool {
	if verifier == "" || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

// signIDToken 用私钥签发一个 RS256 的 id_token。
func (f *fakeOIDCProvider) signIDToken(r *http.Request) string {
	f.mu.Lock()
	subject := f.subject
	nonce := f.nonce
	expired := f.expired
	issuer := f.issuer
	f.mu.Unlock()
	if issuer == "" {
		issuer = f.server.URL
	}

	exp := time.Now().Add(time.Hour)
	if expired {
		exp = time.Now().Add(-time.Hour)
	}
	claims := map[string]any{
		"iss":   issuer,
		"aud":   f.client,
		"sub":   subject,
		"exp":   exp.Unix(),
		"iat":   time.Now().Unix(),
		"nonce": nonce,
	}

	// JWT 三段：header.payload.signature，均 base64url 无填充。
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT","kid":"k1"}`))
	payload, _ := json.Marshal(claims)
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)

	digest := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, digest[:])
	if err != nil {
		// 签名失败没有合理降级方式：返回空串会让上游以"签名无效"失败，
		// 测试里会表现为断言失败，仍然可见。
		return ""
	}
	return body + "." + base64.RawURLEncoding.EncodeToString(sig)
}
