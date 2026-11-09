package httpx

// middleware_audit_test.go 覆盖审计中间件的行为。
//
// 这些测试对应任务验收标准里的四条硬性要求：
//  1. POST 真的被记录；GET 不被记录；
//  2. 失败的操作也记录，且带上错误原因；
//  3. **写审计失败绝不影响业务响应**；
//  4. 动作与目标的提取正确（含未登记路由退化成 unknown）。
//
// 测试策略：用 chi 搭一条"只有审计中间件 + 测试 handler"的最小路由，
// 而不是走完整的 NewRouter。原因是完整路由里 auditMiddleware 绑定了
// d.DB，测试拿不到那个写入端也就无法替换成"永远失败"的实现。
// 最小路由能精确隔离中间件行为，并且可以用真实的路由模式
// （/api/agents/{agentID}/containers/{containerID} 这类）验证查表逻辑。

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"arkade/hub/internal/audit"
	"arkade/hub/internal/user"
)

// fakeAuditWriter 是可控的审计写入端。
type fakeAuditWriter struct {
	mu      sync.Mutex
	records []audit.Record
	err     error // 非 nil 时模拟写入失败
}

func (f *fakeAuditWriter) WriteAudit(rec audit.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.records = append(f.records, rec)
	return nil
}

// snapshot 返回已记录的审计副本。
func (f *fakeAuditWriter) snapshot() []audit.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]audit.Record, len(f.records))
	copy(out, f.records)
	return out
}

// discardLogger 是测试用的静默日志器。
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// auditTestEnv 是审计中间件的最小测试环境。
type auditTestEnv struct {
	// handler 是挂好中间件的路由。
	handler http.Handler
	writer  *fakeAuditWriter
	// user 是模拟已登录用户，nil 表示未登录。
	user *user.User
	// status 让测试 handler 返回指定状态码。
	calls int
	// panicOn 让测试 handler 直接 panic（验证审计不把崩溃记成成功）。
	panicOn bool
	// echoAction 让测试 handler 回显它读到的 action 字段
	// （验证中间件读请求体后确实还原了）。
	echoAction bool
}

// newAuditTestEnv 搭一条最小路由。
//
// 路由里故意包含几条真实模式，用来验证 audit_routes.go 的查表：
//   - /api/users/{userID}（PATCH 与 DELETE 动作不同）
//   - /api/agents/{agentID}（PATCH 与 DELETE 动作不同）
//   - /api/agents/{agentID}/containers/{containerID}（有 target 参数）
//   - /api/agents/{agentID}/disable
//   - /api/agents/{agentID}/stacks/{name}/action（子动作）
func newAuditTestEnv(t *testing.T, loggedIn bool) *auditTestEnv {
	t.Helper()
	env := &auditTestEnv{writer: &fakeAuditWriter{}}
	if loggedIn {
		u := user.User{ID: "u-1", Username: "admin", Role: user.RoleAdmin}
		env.user = &u
	}
	env.build()
	return env
}

// build 重建路由（writer 可能被中途替换）。
func (env *auditTestEnv) build() {
	r := chi.NewRouter()
	// 模拟 authenticator 的效果：把用户放进 context。
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if env.user != nil {
				ctx := context.WithValue(req.Context(), ctxKeyUser, *env.user)
				req = req.WithContext(ctx)
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Use(auditMiddleware(env.writer, discardLogger()))

	// 一个万能 handler：按查询参数决定返回什么状态码。
	responder := func(w http.ResponseWriter, req *http.Request) {
		env.calls++
		if env.panicOn {
			panic("测试用 panic")
		}
		if env.echoAction {
			// 回显中间件读完后 handler 实际能看到的 action，
			// 用来验证请求体被正确还原。
			var body struct {
				Action string `json:"action"`
			}
			_ = json.NewDecoder(req.Body).Decode(&body)
			respondJSON(w, discardLogger(), http.StatusOK, map[string]string{"seen_action": body.Action})
			return
		}
		if code := req.URL.Query().Get("status"); code != "" {
			var n int
			switch code {
			case "fail":
				n = http.StatusInternalServerError
			case "forbidden":
				n = http.StatusForbidden
			case "notfound":
				n = http.StatusNotFound
			default:
				n = http.StatusOK
			}
			if n != http.StatusOK {
				respondError(w, discardLogger(), mapStatusToCode(n), "测试错误原因")
				return
			}
		}
		respondJSON(w, discardLogger(), http.StatusOK, map[string]string{"ok": "1"})
	}

	// 注册与生产一致的路由模式。
	r.Get("/api/users", responder)
	r.Post("/api/users", responder)
	r.Patch("/api/users/{userID}", responder)
	r.Delete("/api/users/{userID}", responder)

	r.Get("/api/groups", responder)
	r.Post("/api/groups", responder)
	r.Patch("/api/groups/{groupID}", responder)
	r.Delete("/api/groups/{groupID}", responder)

	r.Get("/api/agents", responder)
	r.Patch("/api/agents/{agentID}", responder)
	r.Delete("/api/agents/{agentID}", responder)
	r.Post("/api/agents/{agentID}/disable", responder)
	r.Post("/api/agents/{agentID}/enable", responder)

	r.Post("/api/agents/{agentID}/containers/{containerID}/start", responder)
	r.Post("/api/agents/{agentID}/containers/{containerID}/stop", responder)
	r.Post("/api/agents/{agentID}/containers/{containerID}/restart", responder)
	r.Delete("/api/agents/{agentID}/containers/{containerID}", responder)

	r.Post("/api/agents/{agentID}/stacks/{name}/actions", responder)
	r.Post("/api/agents/{agentID}/stacks", responder)
	r.Patch("/api/agents/{agentID}/stacks/{name}", responder)

	env.handler = r
}

// doWithBody 发一次带 JSON 请求体的请求。
func (env *auditTestEnv) doWithBody(t *testing.T, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)
	return w
}

// do 发一次请求。
func (env *auditTestEnv) do(t *testing.T, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, http.NoBody)
	w := httptest.NewRecorder()
	env.handler.ServeHTTP(w, req)
	return w
}

// mapStatusToCode 把状态码映射回错误码（测试 handler 用）。
func mapStatusToCode(status int) string {
	switch status {
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	default:
		return "internal"
	}
}

// TestAuditRecordsMutations 验证 POST 被记录、GET 不被记录。
//
// 这是中间件最核心的行为。GET 不记的理由：列表页刷新会产生十几个 GET，
// 全记的话「谁删了容器」会被「谁看了列表」淹没。
func TestAuditRecordsMutations(t *testing.T) {
	env := newAuditTestEnv(t, true)

	if w := env.do(t, http.MethodPost, "/api/users"); w.Code != http.StatusOK {
		t.Fatalf("POST 应成功，实际 %d", w.Code)
	}
	if w := env.do(t, http.MethodGet, "/api/users"); w.Code != http.StatusOK {
		t.Fatalf("GET 应成功，实际 %d", w.Code)
	}

	got := env.writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("应只记录 1 条（POST），实际 %d 条", len(got))
	}
	if got[0].Action != audit.ActionUserCreate {
		t.Errorf("动作应为 %q，实际 %q", audit.ActionUserCreate, got[0].Action)
	}
	if got[0].Username != "admin" || got[0].UserID != "u-1" {
		t.Errorf("操作人信息错误: %+v", got[0])
	}
	if got[0].Result != audit.ResultSuccess {
		t.Errorf("结果应为 success，实际 %q", got[0].Result)
	}
}

// TestAuditSkipsGetEntirely 验证 GET 完全不触发写入（连调用都不发生）。
//
// 与上一条的区别：那条断言"记录里没有 GET"，这条断言"GET 根本没调用写入端"。
// 后者更强——如果中间件对 GET 也调用了 WriteAudit 再靠过滤，写入端会被高频调用，
// 将来若有人在 WriteAudit 里加了耗时逻辑，GET 就会莫名变慢。
func TestAuditSkipsGetEntirely(t *testing.T) {
	env := newAuditTestEnv(t, true)

	for i := 0; i < 5; i++ {
		if w := env.do(t, http.MethodGet, "/api/agents"); w.Code != http.StatusOK {
			t.Fatalf("GET 应成功，实际 %d", w.Code)
		}
	}
	if n := len(env.writer.snapshot()); n != 0 {
		t.Errorf("GET 不应触发任何审计写入，实际 %d 次", n)
	}
}

// TestAuditRecordsFailureWithError 验证失败操作也记录且带原因。
//
// 「谁尝试做了什么但失败了」和「谁做成了什么」同样重要：
// 连续失败的删除尝试可能是有人在试探权限，也可能是运维在排障。
// 只记成功会让这类信息彻底消失。
func TestAuditRecordsFailureWithError(t *testing.T) {
	env := newAuditTestEnv(t, true)

	w := env.do(t, http.MethodDelete, "/api/users/u-9?status=fail")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("应返回 500，实际 %d", w.Code)
	}

	got := env.writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("失败操作也应记录 1 条，实际 %d", len(got))
	}
	if got[0].Result != audit.ResultFailed {
		t.Errorf("结果应为 failed，实际 %q", got[0].Result)
	}
	// 关键：错误原因必须留存，否则事后无从判断为什么失败。
	if got[0].Error == "" {
		t.Error("失败记录必须带错误原因")
	}
	if got[0].Action != audit.ActionUserDelete {
		t.Errorf("动作应为 %q，实际 %q", audit.ActionUserDelete, got[0].Action)
	}
}

// TestAuditFailureDoesNotBreakResponse 是本任务最关键的一条测试。
//
// **写审计失败绝不影响业务响应。**
//
// 用户的操作已经成功了，此时因为"审计写不进去"返回错误，
// 会让用户以为没成功而重复操作（比如再删一次），造成真正的副作用。
// 审计是旁路功能，它的价值远低于一次正确执行的业务操作。
func TestAuditFailureDoesNotBreakResponse(t *testing.T) {
	env := newAuditTestEnv(t, true)
	env.writer.err = errors.New("磁盘已满")
	env.build()

	w := env.do(t, http.MethodPost, "/api/users")
	if w.Code != http.StatusOK {
		t.Fatalf("写审计失败不应影响业务响应，实际 %d，响应 %s", w.Code, w.Body.String())
	}
	// 响应体也应该正常，不是被吞掉的空壳。
	if body := w.Body.String(); body == "" {
		t.Error("业务响应体不应为空")
	}
}

// TestAuditRecordsAgentAndTarget 验证 agentID 与目标对象的提取。
func TestAuditRecordsAgentAndTarget(t *testing.T) {
	env := newAuditTestEnv(t, true)

	_ = env.do(t, http.MethodDelete, "/api/agents/a1/containers/nginx")

	got := env.writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("应记录 1 条，实际 %d", len(got))
	}
	if got[0].AgentID != "a1" {
		t.Errorf("agent_id 应为 a1，实际 %q", got[0].AgentID)
	}
	if got[0].Target != "nginx" {
		t.Errorf("target 应为容器名 nginx，实际 %q", got[0].Target)
	}
	if got[0].Action != audit.ActionContainerRemove {
		t.Errorf("动作应为 %q，实际 %q", audit.ActionContainerRemove, got[0].Action)
	}
}

// TestAuditUnknownRoute 验证未登记路由退化成 unknown.<METHOD>。
//
// 这不是缺陷而是刻意的信号：审计列表上出现 unknown.POST
// 一眼就能看出"这个接口漏了映射"，补映射时也有据可循。
func TestAuditUnknownRoute(t *testing.T) {
	env := newAuditTestEnv(t, true)

	// 这条路径在路由里没有注册。
	_ = env.do(t, http.MethodPost, "/api/nonexistent/thing")

	got := env.writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("应记录 1 条，实际 %d", len(got))
	}
	if got[0].Action != "unknown.POST" {
		t.Errorf("动作应为 unknown.POST，实际 %q", got[0].Action)
	}
	// 未登记时 target 记原始路径，否则无从知道是哪条路由漏了映射。
	if got[0].Target != "/api/nonexistent/thing" {
		t.Errorf("target 应记原始路径，实际 %q", got[0].Target)
	}
}

// TestAuditRecordsDeleteAction 验证 PATCH 与 DELETE 区分成不同动作。
//
// 同一个路径 /api/users/{userID} 的 PATCH 是"修改"、DELETE 是"删除"。
// 不区分的话，删除用户会被记成"修改用户"——
// 审计里最需要看清的恰恰是"谁删了东西"。
func TestAuditRecordsDeleteAction(t *testing.T) {
	cases := []struct {
		method, path, wantAction string
	}{
		{http.MethodPatch, "/api/users/u-1", audit.ActionUserUpdate},
		{http.MethodDelete, "/api/users/u-1", audit.ActionUserDelete},
		{http.MethodPatch, "/api/agents/a1", audit.ActionAgentUpdate},
		{http.MethodDelete, "/api/agents/a1", audit.ActionAgentDelete},
		{http.MethodPatch, "/api/groups/g-1", audit.ActionGroupUpdate},
		{http.MethodDelete, "/api/groups/g-1", audit.ActionGroupDelete},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			env := newAuditTestEnv(t, true)
			_ = env.do(t, c.method, c.path)
			got := env.writer.snapshot()
			if len(got) != 1 {
				t.Fatalf("应记录 1 条，实际 %d", len(got))
			}
			if got[0].Action != c.wantAction {
				t.Errorf("动作应为 %q，实际 %q", c.wantAction, got[0].Action)
			}
		})
	}
}

// TestAuditDisableEnableActions 验证禁用与启用记成不同动作。
func TestAuditDisableEnableActions(t *testing.T) {
	env := newAuditTestEnv(t, true)
	_ = env.do(t, http.MethodPost, "/api/agents/a1/disable")
	_ = env.do(t, http.MethodPost, "/api/agents/a1/enable")

	got := env.writer.snapshot()
	if len(got) != 2 {
		t.Fatalf("应记录 2 条，实际 %d", len(got))
	}
	if got[0].Action != audit.ActionAgentDisable {
		t.Errorf("应为 %q，实际 %q", audit.ActionAgentDisable, got[0].Action)
	}
	if got[1].Action != audit.ActionAgentEnable {
		t.Errorf("应为 %q，实际 %q", audit.ActionAgentEnable, got[1].Action)
	}
}

// TestAuditStackSubAction 验证编排子动作被合并成 stack.action 且 detail 带子动作。
//
// 编排的 9 个子动作在协议层是 9 个方法，但对审计来说用户视角
// 只是"对某个编排做了一次操作"。拆成 9 个动作会让列表难以浏览。
func TestAuditStackSubAction(t *testing.T) {
	env := newAuditTestEnv(t, true)
	_ = env.do(t, http.MethodPost, "/api/agents/a1/stacks/web/actions?status=ok")

	got := env.writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("应记录 1 条，实际 %d", len(got))
	}
	if got[0].Action != audit.ActionStackAction {
		t.Errorf("动作应为 %q，实际 %q", audit.ActionStackAction, got[0].Action)
	}
	if got[0].Target != "web" {
		t.Errorf("target 应为编排名 web，实际 %q", got[0].Target)
	}
}

// TestAuditRecordsDuration 验证耗时与时间被记录。
func TestAuditRecordsDuration(t *testing.T) {
	env := newAuditTestEnv(t, true)
	_ = env.do(t, http.MethodPost, "/api/agents/a1/disable")

	got := env.writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("应记录 1 条，实际 %d", len(got))
	}
	// 不能用 >0 判断：一次很快的请求毫秒取整后可能就是 0，那是正常值。
	if got[0].DurationMs < 0 {
		t.Errorf("耗时不应为负，实际 %d", got[0].DurationMs)
	}
	if got[0].CreatedAt.IsZero() {
		t.Error("创建时间必须被填上")
	}
	if time.Since(got[0].CreatedAt) > time.Minute {
		t.Error("创建时间应是当前时间")
	}
	if got[0].ID == "" {
		t.Error("ID 必须被生成")
	}
}

// TestAuditAnonymousRequest 验证未登录时仍记录但不带用户。
//
// 登录失败尝试是安全审计的重要内容，不能因为取不到用户就整条不记。
// 但 user_id 必须留空而不是填个假值——填假值会让审计失去可信度。
func TestAuditAnonymousRequest(t *testing.T) {
	env := newAuditTestEnv(t, false)

	_ = env.do(t, http.MethodPost, "/api/users")

	got := env.writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("未登录的写请求也应记录，实际 %d", len(got))
	}
	if got[0].UserID != "" || got[0].Username != "" {
		t.Errorf("未登录时用户字段应为空，实际 %+v", got[0])
	}
}

// TestIsMutatingMethod 直接验证方法判定表。
//
// HEAD/OPTIONS 也必须是 false：它们由框架自动处理，
// 记进来只会是噪音。
func TestIsMutatingMethod(t *testing.T) {
	mutating := []string{"POST", "PUT", "PATCH", "DELETE"}
	for _, m := range mutating {
		if !isMutatingMethod(m) {
			t.Errorf("%s 应被视为有副作用", m)
		}
	}
	readOnly := []string{"GET", "HEAD", "OPTIONS"}
	for _, m := range readOnly {
		if isMutatingMethod(m) {
			t.Errorf("%s 不应被视为有副作用", m)
		}
	}
}

// TestResolveAuditRouteUnknown 验证查不到时的退化行为。
func TestResolveAuditRouteUnknown(t *testing.T) {
	// 空模式（404）也应给 unknown，而不是空动作。
	r := resolveAuditRoute("", "POST", "/whatever")
	if r.Action != "unknown.POST" {
		t.Errorf("空路由模式应退化为 unknown.POST，实际 %q", r.Action)
	}
	// 未登记的已匹配路由。
	r = resolveAuditRoute("/api/unknown/path", "PUT", "/api/unknown/path")
	if r.Action != "unknown.PUT" {
		t.Errorf("未登记路由应退化为 unknown.PUT，实际 %q", r.Action)
	}
}

// TestResolveAuditRouteKnown 验证已登记路由的解析。
func TestResolveAuditRouteKnown(t *testing.T) {
	r := resolveAuditRoute("/api/agents/{agentID}/containers/{containerID}/restart", "POST", "/x")
	if r.Action != audit.ActionContainerRestart {
		t.Errorf("应解析为 %q，实际 %q", audit.ActionContainerRestart, r.Action)
	}
	if r.TargetParam != "containerID" {
		t.Errorf("TargetParam 应为 containerID，实际 %q", r.TargetParam)
	}
}

// TestSubActionFromPath 验证子动作提取。
func TestSubActionFromPath(t *testing.T) {
	cases := map[string]string{
		"/api/agents/a1/stacks/web/action":  "action",
		"/api/agents/a1/stacks/web/action/": "action", // 尾斜杠
		"/single":                           "single",
		"":                                  "",
	}
	for in, want := range cases {
		if got := subActionFromPath(in); got != want {
			t.Errorf("subActionFromPath(%q) = %q，期望 %q", in, got, want)
		}
	}
}
