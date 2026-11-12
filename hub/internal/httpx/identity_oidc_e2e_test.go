// identity_oidc_e2e_test.go 用真实 HTTP 跑 OIDC 闭环的装配与请求工具。
//
// 这个文件只放"怎么发请求"：环境装配、Cookie 处理、假提供方交互。
// 具体断言分在 identity_oidc_login_test.go / bind_test.go / settings_test.go，
// 拆开是为了让"环境坏了"和"行为错了"能一眼区分。
//
// 覆盖的是接线：Deps.Identity 装配、路由绑定、中间件顺序、Cookie 下发与校验、
// 审计落库——这些只有整条链路跑起来才看得见。
//
// 假提供方见 identity_oidc_provider_test.go：真实 RS256 签名、PKCE S256、
// nonce 与授权码一次性校验，go-oidc / oauth2 走的是真实代码路径。

package httpx

import (
	"arkade/hub/internal/testdb"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/auth"
	accountservice "arkade/hub/internal/service/account"
	oidcservice "arkade/hub/internal/service/oidclogin"
	settingsservice "arkade/hub/internal/service/settings"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// oidcTestEnv 是一套装配了完整 identity 的测试环境。
//
// 不复用 newTestEnv：那个 helper 固定不传 Deps.Identity，
// 而本文件要验的恰恰是"Identity 装好之后的行为"。
// 这里直接构造 NewRouter(Deps{...})，是 NewRouter 的公开入口，
// 不需要改动任何现有 helper、路由或 runtime。
type oidcTestEnv struct {
	base        string // 真实监听地址，如 http://127.0.0.1:53211
	db          *store.DB
	auth        *auth.Service
	settings    *settingsservice.Service
	provider    *fakeOIDCProvider
	adminCookie *http.Cookie
	adminToken  string
	// client 不自动跟随重定向：OIDC 流程里 302 的 Location 和 Set-Cookie
	// 本身就是被测对象，必须逐跳观察。
	client *http.Client
}

// newOIDCTestEnv 组装并启动带 identity 的真实服务器。
func newOIDCTestEnv(t *testing.T) *oidcTestEnv {
	t.Helper()

	ctx := context.Background()
	dsn := testdb.New(t)

	db, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 管理员账号：后续用它登录拿到会话，再以管理员身份配置 provider。
	hash, err := user.HashPassword("TestPass123!")
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	now := time.Now().UTC()
	if err := db.CreateUser(ctx, user.User{
		ID: "u-admin", Username: "admin", DisplayName: "管理员",
		PasswordHash: hash, Role: user.RoleAdmin,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("创建管理员失败: %v", err)
	}

	authSvc := auth.NewService(db, time.Hour)
	// 与生产一致：策略也必须装上，否则会话 TTL 走默认值、与真实路径不同。
	authSvc.SetPolicy(accountservice.New(db).AuthPolicy)

	// settings 服务要 32 字节密钥。这里直接生成随机 32 字节，
	// 不走 loadSettingsKey 的文件读写——那属于配置加载，不是本次验收面。
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("生成设置密钥失败: %v", err)
	}

	// AllowInsecureLoopback=true：假提供方是 http://127.0.0.1，
	// 没有它 issuer 校验会直接拒绝，流程根本走不到被测逻辑。
	settings, err := settingsservice.NewWithOptions(db, key, "http://127.0.0.1", settingsservice.Options{AllowInsecureLoopback: true})
	if err != nil {
		t.Fatalf("初始化设置服务失败: %v", err)
	}

	fake := newFakeOIDCProvider(t, "hub-client", "external-user")

	// LoadProvider 必须真的从 settings 读：
	// 这样"管理员通过 HTTP 创建 provider"才会真正影响 OIDC 登录，
	// 而不是由一个写死的函数返回固定值（那样测不出接线）。
	oidc := oidcservice.New(oidcservice.Deps{
		DB:   db,
		Auth: authSvc,
		LoadProvider: func(ctx context.Context, id string) (oidcservice.Credentials, error) {
			v, err := settings.LoadProvider(ctx, id)
			return oidcservice.Credentials{
				Provider: v.Provider, ClientSecret: v.ClientSecret,
				RedirectURL: v.RedirectURL, AllowInsecureLoopback: v.AllowInsecureLoopback,
			}, err
		},
	})

	// agents 列表依赖 Registry/Ops：不装会 nil panic（不是"没数据"而是 500）。
	// 生产装配里它们总是存在，测试也要给，否则测不到真实行为。
	reg := agentlink.NewRegistry()
	ops := agentlink.NewOps(reg)

	router := NewRouter(Deps{
		Log:        testDiscardLogger(),
		DB:         db,
		Auth:       authSvc,
		WebDist:    "",
		HubVersion: "test",
		Identity:   &identityServices{settings: settings, oidc: oidc, secure: false},
		Registry:   reg,
		Ops:        ops,
	})
	base := StartForTest(t, router)

	env := &oidcTestEnv{
		base: base, db: db, auth: authSvc, settings: settings, provider: fake,
		client: &http.Client{
			// 不自动跟随：302 的 Location 与 Set-Cookie 是断言对象。
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       10 * time.Second,
		},
	}
	env.loginAdmin(t)
	return env
}

// loginAdmin 用本地密码登录，把会话 Cookie 存进 env。
func (e *oidcTestEnv) loginAdmin(t *testing.T) {
	t.Helper()
	resp := e.post(t, "/api/auth/login", map[string]any{
		"username": "admin", "password": "TestPass123!",
	}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("管理员登录失败: %d %s", resp.StatusCode, e.body(resp))
	}
	ck := findCookie(resp, "arkade_session")
	if ck == nil {
		t.Fatalf("登录响应没有下发会话 Cookie，响应体=%s", e.body(resp))
	}
	e.adminCookie = ck
	e.adminToken = ck.Value
}

// createProvider 以管理员身份通过 HTTP 创建 OIDC provider。
//
// 这一步本身也是被测项：它验证 settings 的写接口在完整路由下可用。
func (e *oidcTestEnv) createProvider(t *testing.T, autoRegister bool) string {
	t.Helper()
	resp := e.post(t, "/api/settings/oidc-providers", map[string]any{
		"display_name":  "假提供方",
		"issuer":        e.provider.URL(),
		"client_id":     "hub-client",
		"client_secret": "test-secret",
		"scopes":        "openid profile email",
		"enabled":       true,
		"auto_register": autoRegister,
	}, []*http.Cookie{e.adminCookie})
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("创建 provider 失败: %d %s", resp.StatusCode, e.body(resp))
	}
	body := e.jsonBody(t, resp)
	view, _ := body["provider"].(map[string]any)
	id, _ := view["id"].(string)
	if id == "" {
		t.Fatalf("创建 provider 的响应里没有 id: %v", body)
	}
	return id
}

// beginOIDC 调 start 拿到授权地址与浏览器绑定 Cookie。
func (e *oidcTestEnv) beginOIDC(t *testing.T, providerID string, cookies []*http.Cookie) (location string, browser *http.Cookie) {
	t.Helper()
	resp := e.get(t, "/api/auth/oidc/"+providerID+"/start", cookies)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("start 期望 302，实际 %d：%s（providerID=%q）", resp.StatusCode, e.body(resp), providerID)
	}
	location = resp.Header.Get("Location")
	if location == "" {
		t.Fatal("start 的 302 没有 Location")
	}
	// 浏览器绑定 Cookie 名字带 state 摘要，这里取第一个 arkade_oidc_ 前缀的。
	for _, c := range resp.Cookies() {
		if strings.HasPrefix(c.Name, "arkade_oidc_") {
			browser = c
			break
		}
	}
	_ = e.body(resp)
	return location, browser
}

// authorizeAtProvider 模拟浏览器访问假提供方的授权页，拿回 callback 地址。
func (e *oidcTestEnv) authorizeAtProvider(t *testing.T, location string) string {
	t.Helper()
	resp, err := e.client.Get(location)
	if err != nil {
		t.Fatalf("访问假提供方失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("假提供方期望 302，实际 %d", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

// testDiscardLogger 返回一个丢弃全部输出的 logger。
//
// 不直接复用 newTestEnv 里的 logger，因为本文件独立装配，
// 不该依赖别的 helper 的内部细节。
func testDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestOIDCEnvSmoke 先验证装配本身可用：服务器起来了、管理员能登录、
// provider 能创建、start 能返回真实 302。
//
// 这条是后面所有断言的前提，单独跑它能让接线问题一次暴露，
// 不用在一堆业务断言里猜是哪一步坏的。
func TestOIDCEnvSmoke(t *testing.T) {
	e := newOIDCTestEnv(t)

	// 1. 未登录也能读公开的登录选项。
	resp := e.get(t, "/api/auth/options", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth/options 期望 200，实际 %d：%s", resp.StatusCode, e.body(resp))
	}
	_ = e.body(resp)

	// 2. provider 创建成功。
	id := e.createProvider(t, false)

	// 3. start 返回指向假提供方的 302，并下发浏览器绑定 Cookie。
	location, browser := e.beginOIDC(t, id, nil)
	if !strings.Contains(location, "/auth?") {
		t.Fatalf("start 的 Location 不指向提供方授权页: %s", location)
	}
	if browser == nil {
		t.Fatal("start 没有下发 state 绑定 Cookie")
	}
	// PKCE 与 nonce 必须真的出现在授权请求里。
	q, err := url.Parse(location)
	if err != nil {
		t.Fatalf("解析 Location 失败: %v", err)
	}
	vals := q.Query()
	if vals.Get("code_challenge") == "" || vals.Get("code_challenge_method") != "S256" {
		t.Fatalf("授权请求缺 PKCE：%s", location)
	}
	if vals.Get("nonce") == "" {
		t.Fatalf("授权请求缺 nonce：%s", location)
	}
}
