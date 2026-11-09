package httpx

// router_test.go 覆盖 Hub 的鉴权契约与路由行为。
//
// 测试原则：断言真实 HTTP 语义（状态码 + 响应体 JSON 结构），
// 而不是断言内部函数返回值——接口契约才是前端真正依赖的东西。

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// testEnv 是测试用的完整 Hub 环境。
type testEnv struct {
	router http.Handler
	db     *store.DB
	auth   *auth.Service
	log    *slog.Logger

	// registry 与 guard 是**真实装配**的一部分，必须与 WebSocket 服务共用。
	//
	// 早先这里每个测试各建一个 Registry，于是出现一类假失败：
	// HTTP 的"重置密钥"踢的是 A 表，而测试的 Agent 连在 B 表上，
	// 即使生产代码正确也测不过；反过来更糟——它会掩盖真实的踢连接 bug。
	registry *agentlink.Registry
	guard    *agentlink.EnrollmentGuard

	// agentHandler 是 Agent 连接入口（供测试在需要时换成阻塞式 handler）。
	agentHandler *agentlink.Server

	// gate 是握手闸门：竞态测试用它把握手停在"已认证、未注册"位置。
	gate *gatedHello

	// wsURL 是同一个 httptest 服务的地址，Agent 与 API 打在同一台服务上，
	// 与生产一致（生产也是同一个端口）。
	wsURL string

	// closeSrv 关闭 httptest 服务。
	closeSrv func()
}

// newTestEnv 搭建一套真实依赖（真实 SQLite 临时文件），只有地址是虚拟的。
// 不 mock store 的原因：迁移脚本、单写连接、WAL 这些恰恰是最容易出错的地方。
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))

	db, err := store.Open(newTestContext(), dbPath)
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 创建测试管理员。
	hash, err := user.HashPassword("TestPass123!")
	if err != nil {
		t.Fatalf("生成密码哈希失败: %v", err)
	}
	now := time.Now().UTC()
	if err := db.CreateUser(newTestContext(), user.User{
		ID:           "u-test",
		Username:     "admin",
		DisplayName:  "管理员",
		PasswordHash: hash,
		Role:         user.RoleAdmin,
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		t.Fatalf("创建测试用户失败: %v", err)
	}

	authSvc := auth.NewService(db, time.Hour)
	reg := agentlink.NewRegistry()
	ops := agentlink.NewOps(reg)
	guard := agentlink.NewEnrollmentGuard()

	// Agent 连接服务：注入与路由相同的 registry 与 guard，
	// 这样撤销类接口（reset/delete/disable）真的能作用到测试的 Agent 连接上。
	//
	// 外面包一层可挂起的 gate：竞态测试需要把握手停在
	// "已认证、未注册"这个精确位置，再插入撤销操作。
	// 平时它直接透传，不影响其它测试。
	gate := &gatedHello{sink: &agentSink{db: db, log: log}}
	agentSrv := agentlink.NewServer(reg, composeTestAgentToken, "test", gate, log)
	agentSrv.SetCredentialStore(NewCredentialStore(db))
	agentSrv.SetEnrollmentGuard(guard)

	router := NewRouter(Deps{
		Log:        log,
		DB:         db,
		Auth:       authSvc,
		Registry:   reg,
		Ops:        ops,
		AgentConn:  http.HandlerFunc(agentSrv.HandleConnect),
		WebDist:    "",
		HubVersion: "test",
		// 与生产一致：handler 需要共享 token 才能"避开同值"生成专属密钥。
		AgentSharedToken: composeTestAgentToken,
		Guard:            guard,
	})

	// 同一个端口同时服务 API 与 Agent 连接（生产也是这样）。
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	return &testEnv{
		router:       router,
		db:           db,
		auth:         authSvc,
		log:          log,
		registry:     reg,
		guard:        guard,
		agentHandler: agentSrv,
		gate:         gate,
		wsURL:        srv.URL,
		closeSrv:     srv.Close,
	}
}

// do 执行一次请求并返回响应与解析后的错误体。
func (e *testEnv) do(t *testing.T, method, path string, body string, cookies []*http.Cookie) (*httptest.ResponseRecorder, errorResponse) {
	t.Helper()

	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	for _, c := range cookies {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)

	var parsed errorResponse
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &parsed)
	}
	return rec, parsed
}

// errorResponse 是错误响应体的测试视图。
type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// login 登录并返回会话 Cookie。
func (e *testEnv) login(t *testing.T) []*http.Cookie {
	t.Helper()
	rec, _ := e.do(t, http.MethodPost, "/api/auth/login",
		`{"username":"admin","password":"TestPass123!"}`, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录应成功，实际状态码 %d，响应 %s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("登录成功但没有下发会话 Cookie")
	}
	return cookies
}

// TestUnauthenticatedReturns401 验证未登录访问业务接口一律 401。
//
// 这是与前端约定死的契约：前端路由守卫靠 code=unauthorized 判断跳登录页，
// 绝不允许返回 200 + 空数据伪装成功。
func TestUnauthenticatedReturns401(t *testing.T) {
	env := newTestEnv(t)

	paths := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/api/agents"},
		{http.MethodGet, "/api/auth/me"},
		{http.MethodGet, "/api/agents/any/containers"},
		{http.MethodPost, "/api/agents/any/containers/abc/start"},
		{http.MethodGet, "/api/agents/any/containers/abc/logs"},
	}

	for _, p := range paths {
		rec, body := env.do(t, p.method, p.path, "", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s 应返回 401，实际 %d", p.method, p.path, rec.Code)
			continue
		}
		if body.Error.Code != "unauthorized" {
			t.Errorf("%s %s 错误码应为 unauthorized，实际 %q", p.method, p.path, body.Error.Code)
		}
	}
}

// TestLoginWrongPassword 验证错误密码不会登录成功，且不泄漏账号是否存在。
func TestLoginWrongPassword(t *testing.T) {
	env := newTestEnv(t)

	rec, body := env.do(t, http.MethodPost, "/api/auth/login",
		`{"username":"admin","password":"wrong-password"}`, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误密码应返回 401，实际 %d", rec.Code)
	}
	if body.Error.Message != "用户名或密码错误" {
		t.Errorf("文案应为「用户名或密码错误」，实际 %q", body.Error.Message)
	}

	// 不存在的用户应得到完全相同的响应，防止账号枚举。
	rec2, body2 := env.do(t, http.MethodPost, "/api/auth/login",
		`{"username":"nobody","password":"wrong-password"}`, nil)
	if rec2.Code != rec.Code || body2.Error.Message != body.Error.Message {
		t.Errorf("不存在的用户与错误密码的响应必须一致，实际 %d/%q vs %d/%q",
			rec2.Code, body2.Error.Message, rec.Code, body.Error.Message)
	}
}

// TestLoginSuccessAndMe 验证登录后能拿到自己的信息。
func TestLoginSuccessAndMe(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodGet, "/api/auth/me", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("/auth/me 应返回 200，实际 %d", rec.Code)
	}

	var me userEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("解析 /auth/me 响应失败: %v", err)
	}
	if me.User.Username != "admin" {
		t.Errorf("用户名应为 admin，实际 %q", me.User.Username)
	}
	if me.User.Role != string(user.RoleAdmin) {
		t.Errorf("角色应为 admin，实际 %q", me.User.Role)
	}
}

// TestLoginRejectsUnknownField 验证请求体多传字段会被拒绝。
// 前端与后端契约漂移必须立刻暴露，而不是静默忽略。
func TestLoginRejectsUnknownField(t *testing.T) {
	env := newTestEnv(t)

	rec, body := env.do(t, http.MethodPost, "/api/auth/login",
		`{"username":"admin","password":"TestPass123!","unexpected":"x"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("未知字段应返回 400，实际 %d", rec.Code)
	}
	if body.Error.Code != "bad_request" {
		t.Errorf("错误码应为 bad_request，实际 %q", body.Error.Code)
	}
}

// TestLogoutInvalidatesSession 验证退出登录后会话立即失效。
func TestLogoutInvalidatesSession(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodPost, "/api/auth/logout", "", cookies)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("退出登录应返回 204，实际 %d", rec.Code)
	}

	// 用旧 Cookie 再访问应被拒绝。
	rec2, body := env.do(t, http.MethodGet, "/api/agents", "", cookies)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("退出后访问应返回 401，实际 %d", rec2.Code)
	}
	if body.Error.Code != "unauthorized" {
		t.Errorf("错误码应为 unauthorized，实际 %q", body.Error.Code)
	}
}

// TestListAgentsEmpty 验证没有 Agent 时返回空数组而不是 null。
// 前端直接把 data.agents 当数组用，返回 null 会让界面崩掉。
func TestListAgentsEmpty(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, _ := env.do(t, http.MethodGet, "/api/agents", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/agents 应返回 200，实际 %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, `"agents":[]`) {
		t.Errorf("空列表应序列化为 []，实际 %s", body)
	}
}

// TestUnknownAPIPathReturnsJSON 验证未知 API 返回 JSON 错误而不是 HTML。
// 前端会尝试 JSON.parse，返回 HTML 会得到难以理解的解析错误。
func TestUnknownAPIPathReturnsJSON(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, body := env.do(t, http.MethodGet, "/api/does-not-exist", "", cookies)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知 API 应返回 404，实际 %d", rec.Code)
	}
	if body.Error.Code != "not_found" {
		t.Errorf("错误码应为 not_found，实际 %q", body.Error.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("未知 API 的响应必须是 JSON，实际 Content-Type=%q", ct)
	}
}

// TestHealthz 验证健康检查不需要认证。
func TestHealthz(t *testing.T) {
	env := newTestEnv(t)

	rec, _ := env.do(t, http.MethodGet, "/healthz", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("健康检查应返回 200，实际 %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("健康检查应返回 status=ok，实际 %s", rec.Body.String())
	}
}

// TestContainerOpOnMissingAgent 验证对不存在的服务器发起操作会返回 404，
// 而不是 agent_offline 或超时。
func TestContainerOpOnMissingAgent(t *testing.T) {
	env := newTestEnv(t)
	cookies := env.login(t)

	rec, body := env.do(t, http.MethodPost, "/api/agents/ghost/containers/abc/start", "", cookies)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("不存在的服务器应返回 404，实际 %d", rec.Code)
	}
	if body.Error.Code != "not_found" {
		t.Errorf("错误码应为 not_found，实际 %q", body.Error.Code)
	}

	// 列表接口同理：必须先确认服务器存在，再谈能否操作。
	rec2, _ := env.do(t, http.MethodGet, "/api/agents/ghost/containers", "", cookies)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("不存在服务器的容器列表应返回 404，实际 %d", rec2.Code)
	}
}

// newTestContext 返回带超时的后台上下文。
func newTestContext() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_ = cancel // 测试结束时进程退出，无需显式取消
	return ctx
}
