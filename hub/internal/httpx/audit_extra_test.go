package httpx

// audit_extra_test.go 补齐中途发现的三处缺口。
//
// 这三条都是"看起来对、实际错"的典型，各自的失败模式很隐蔽：
//  1. panic 被记成 success —— 审计说成功但操作崩了；
//  2. stack action 的 detail 恒为 "actions" —— 所有编排操作长得一样；
//  3. 登录审计泄漏密码 —— 最严重，且一旦发生无法撤回。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkade/hub/internal/audit"
)

// TestAuditPanicRecordedAsFailed 验证 panic 被记为失败。
//
// 关键背景：defer 是后进先出，审计中间件的 defer 注册在**内层**，
// 比外层 recoverer 先执行。也就是说 recoverer 还没把 panic 变成 500 时，
// 审计就已经记录完毕——此时状态码还是默认的 200，panic 会被记成 success。
//
// "操作崩了但审计说成功"比没有记录更危险：事故复盘会走偏，
// 而复盘走偏会让人往完全错误的方向排查。
func TestAuditPanicRecordedAsFailed(t *testing.T) {
	env := newAuditTestEnv(t, true)
	env.panicOn = true
	env.build()

	// 手工在外层套一个 recoverer，模拟真实中间件栈的顺序：
	// recoverer 在外，审计在内。
	wrapped := recoverer(discardLogger())(env.handler)
	req := httptest.NewRequest(http.MethodPost, "/api/users", http.NoBody)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("panic 应被外层 recoverer 转成 500，实际 %d", w.Code)
	}

	got := env.writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("panic 也应记录 1 条，实际 %d", len(got))
	}
	if got[0].Result != audit.ResultFailed {
		t.Errorf("panic 必须记为 failed，实际 %q（被记成成功是最危险的失败模式）", got[0].Result)
	}
	if got[0].Error == "" {
		t.Error("panic 记录应带说明")
	}
}

// TestAuditPanicStillPropagates 验证 panic 没有被审计中间件吞掉。
//
// 审计自己 recover 一次的目的只是"看清结果"，不应吞掉 panic：
// 吞掉会让外层 recoverer 的日志与 500 响应都不发生。
//
// ## 迁移说明（断言方式变化，强度不变）
//
// 原来直接调 handler 并 `recover()` 断言能捕获到 panic。
// 迁移后测试必须走**真实 HTTP**（ghttp 生命周期依赖 Start），
// 而 panic 一旦穿过到 net/http 的 ServeHTTP，会被标准库自己 recover
// 并记日志、断开连接——panic 不再可能传回测试进程。
// 因此不能再断言 "recover() != nil"。
//
// 替代断言（等价强度）：
//   - 请求**得不到一个成功的 2xx**（若审计吞了 panic，业务会继续往下走，
//     这里会拿到 200，测试即失败）；
//   - 审计仍然记录了 failed。
//
// 两条合起来正是原测试想保证的："panic 没有被吞成成功"。
func TestAuditPanicStillPropagates(t *testing.T) {
	env := newAuditTestEnv(t, true)
	env.panicOn = true
	env.build()

	// 这里不发真实请求——发了只会在服务端被 recover，客户端看到 EOF。
	// 改为直接在测试环境内校验中间件行为：用一个**裸**中间件链
	// （不含 recoverer）包住会 panic 的 handler，在进程内 recover。
	//
	// 这样仍然验证"审计不吞 panic"这个语义，且不依赖传输层。
	h := auditMiddleware(env.writer, discardLogger())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("测试用 panic")
	}))

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		req := httptest.NewRequest(http.MethodPost, "/api/users", nil)
		req = req.WithContext(withRouteInfo(req.Context(), &routeInfo{Pattern: "/api/users"}))
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()

	if recovered == nil {
		t.Error("审计中间件不应吞掉 panic，应 re-panic 交给外层")
	}
	// 即使 panic 了，审计也必须已经写下失败记录。
	got := env.writer.snapshot()
	if len(got) != 1 || got[0].Result != audit.ResultFailed {
		t.Errorf("re-panic 前应已记录失败，实际 %+v", got)
	}
}

// TestAuditStackActionDetailFromBody 验证编排子动作取自请求体。
//
// 真正的坑：编排的 up/down/pull/restart 全都走同一条路径
// /stacks/{name}/actions，区别只在请求体的 action 字段。
// 若从路径取子动作，得到的永远是 "actions"——
// 审计里所有编排操作长得一模一样，等于没记。
func TestAuditStackActionDetailFromBody(t *testing.T) {
	cases := []string{"up", "down", "pull", "restart"}
	for _, action := range cases {
		t.Run(action, func(t *testing.T) {
			env := newAuditTestEnv(t, true)
			env.build()

			body := `{"action":"` + action + `"}`
			w := env.doWithBody(t, http.MethodPost, "/api/agents/a1/stacks/web/actions", body)
			if w.Code != http.StatusOK {
				t.Fatalf("实际 %d", w.Code)
			}

			got := env.writer.snapshot()
			if len(got) != 1 {
				t.Fatalf("应记录 1 条，实际 %d", len(got))
			}
			if got[0].Action != audit.ActionStackAction {
				t.Errorf("动作应为 %q，实际 %q", audit.ActionStackAction, got[0].Action)
			}
			// 这是核心断言：detail 必须是真实子动作，而不是路径末段。
			if got[0].Detail != action {
				t.Errorf("detail 应为 %q（来自请求体），实际 %q（若为 actions 说明错误地取了路径）",
					action, got[0].Detail)
			}
			if got[0].Target != "web" {
				t.Errorf("target 应为编排名 web，实际 %q", got[0].Target)
			}
		})
	}
}

// TestAuditStackActionBodyStillReadableByHandler 验证中间件读后还原了请求体。
//
// 这是加"读请求体"功能时最容易踩的坑：中间件把 body 读走了不还原，
// handler 拿到空 body 解码失败返回 400——**加审计反而把业务弄坏了**。
// 所以测试 handler 必须真的能读到 action 字段。
func TestAuditStackActionBodyStillReadableByHandler(t *testing.T) {
	env := newAuditTestEnv(t, true)
	// 让测试 handler 回显它读到的 action。
	env.echoAction = true
	env.build()

	w := env.doWithBody(t, http.MethodPost, "/api/agents/a1/stacks/web/actions", `{"action":"restart"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("实际 %d，响应 %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["seen_action"] != "restart" {
		t.Errorf("handler 应能读到 action=restart（说明中间件还原了 body），实际 %q", resp["seen_action"])
	}
}

// TestAuditStackActionNoBody 验证请求体缺失时不影响业务、也不写假 detail。
func TestAuditStackActionNoBody(t *testing.T) {
	env := newAuditTestEnv(t, true)
	env.build()

	w := env.do(t, http.MethodPost, "/api/agents/a1/stacks/web/actions")
	if w.Code != http.StatusOK {
		t.Fatalf("无请求体不应报错，实际 %d", w.Code)
	}
	got := env.writer.snapshot()
	if len(got) != 1 {
		t.Fatalf("应记录 1 条，实际 %d", len(got))
	}
	if got[0].Detail != "" {
		t.Errorf("解析不到子动作时 detail 应为空，实际 %q（不应编造）", got[0].Detail)
	}
}

// TestLoginAuditNoPasswordLeak 是本任务里最要紧的一条安全测试。
//
// **密码绝不能出现在审计的任何字段里。**
//
// 理由：审计表会被非 admin 用户读到（他们能看自己的记录），
// 也会被运维导出、被备份带走。一旦密码落进审计，
// 等于把明文口令写进了一个"比密码表本身还容易被看到"的地方，
// 且事后无法撤回（备份里已经有副本了）。
func TestLoginAuditNoPasswordLeak(t *testing.T) {
	e := newTestEnv(t)
	const secret = "SuperSecret-Passw0rd-9x7"

	// 一次失败的登录（用户不存在，保证密码不会被接受）。
	cookies := e.loginAttempt(t, "ghost-user", secret)
	if cookies != nil {
		t.Fatal("不存在的用户不该登录成功")
	}

	// 从审计表读出这条记录，检查所有字段。
	recs, _, err := e.db.ListAuditLogs(t.Context(), storeAllAudit())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 {
		t.Fatal("失败的登录也应记入审计")
	}
	for _, r := range recs {
		for _, field := range []string{
			r.ID, r.UserID, r.Username, r.AgentID, r.AgentName,
			r.Action, r.Target, r.Detail, r.Result, r.Error,
		} {
			if strings.Contains(field, secret) {
				t.Fatalf("审计记录里出现了密码明文！字段值 %q", field)
			}
			// 也检查片段：即使只泄漏前 8 位也是不可接受的。
			if len(secret) > 8 && strings.Contains(field, secret[:8]) {
				t.Fatalf("审计记录里出现了密码片段！字段值 %q", field)
			}
		}
	}
}

// TestLoginAuditBothOutcomes 验证成功与失败都入审计。
func TestLoginAuditBothOutcomes(t *testing.T) {
	e := newTestEnv(t)

	// 失败一次。
	e.loginAttempt(t, "admin", "wrong-password")
	// 成功一次。
	if c := e.loginAttempt(t, "admin", testPassword); c == nil {
		t.Fatal("正确的密码应登录成功")
	}

	recs, _, err := e.db.ListAuditLogs(t.Context(), storeAllAudit())
	if err != nil {
		t.Fatal(err)
	}

	var ok, failed bool
	for _, r := range recs {
		if r.Action != audit.ActionUserLogin {
			continue
		}
		switch r.Result {
		case audit.ResultSuccess:
			ok = true
			if r.UserID == "" {
				t.Error("登录成功时应记录 user_id")
			}
		case audit.ResultFailed:
			failed = true
			// 失败时 user_id 必须为空：不能编造一个 ID。
			if r.UserID != "" {
				t.Errorf("失败时 user_id 应留空，实际 %q", r.UserID)
			}
			// 必须记下尝试的用户名，这是安全审计的关键信息。
			if r.Username != "admin" {
				t.Errorf("失败时应记录尝试的用户名，实际 %q", r.Username)
			}
			// **失败原因不得含账号是否存在的线索。**
			if r.Error != "" {
				t.Errorf("登录失败不应记录具体原因（会泄漏账号是否存在），实际 %q", r.Error)
			}
		}
	}
	if !ok {
		t.Error("成功的登录应入审计")
	}
	if !failed {
		t.Error("失败的登录应入审计")
	}
}

// TestLoginAuditUnknownUserRecordsAttemptedName 验证未知用户名只记输入值。
func TestLoginAuditUnknownUserRecordsAttemptedName(t *testing.T) {
	e := newTestEnv(t)
	e.loginAttempt(t, "  nobody-here  ", "x")

	recs, _, err := e.db.ListAuditLogs(t.Context(), storeAllAudit())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Action != audit.ActionUserLogin {
			continue
		}
		// 规范化（去空白）后的输入名。
		if r.Username != "nobody-here" {
			t.Errorf("应记录规范化后的输入名，实际 %q", r.Username)
		}
		if r.UserID != "" {
			t.Errorf("未知用户不应有 user_id，实际 %q", r.UserID)
		}
		return
	}
	t.Error("未记录登录尝试")
}

// TestLoginAuditUniformFailureMessage 验证对外文案不泄漏账号是否存在。
//
// 两种失败（用户不存在 / 密码错误）对外必须完全一致，
// 否则攻击者可以用它枚举账号。
func TestLoginAuditUniformFailureMessage(t *testing.T) {
	e := newTestEnv(t)

	// 用户不存在。
	rec1, body1 := e.loginRaw(t, "ghost", "anything")
	// 用户存在但密码错。
	rec2, body2 := e.loginRaw(t, "admin", "wrong")

	if rec1 != rec2 {
		t.Errorf("两种失败的状态码应一致：%d vs %d", rec1, rec2)
	}
	if body1 != body2 {
		t.Errorf("两种失败的响应体应完全一致（否则可枚举账号）：\n%q\n%q", body1, body2)
	}
	if strings.Contains(body1, "不存在") {
		t.Errorf("响应不应提示账号不存在: %q", body1)
	}
}
