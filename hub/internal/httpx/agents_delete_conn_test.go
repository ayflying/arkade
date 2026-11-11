package httpx

// agents_delete_conn_test.go 覆盖「删除服务器记录后断开存量连接」。
//
// 这是真机验证踩到的缺陷，值得为它单独一组测试：
// 删完记录后 GET /api/agents 返回 []，但 /healthz 的 agents_online
// 仍为 1，Agent 的 WebSocket 也还活着。
//
// 后果链条：
//   1. 在线表里留着一条没有对应记录的连接 —— "幽灵在线"；
//   2. 该 Agent 不知道自己的记录已被移除，因此不会重连，
//      也就永远等不到"重新注册"的机会；
//   3. 界面上"删掉了却还显示在线"，而操作该服务器的接口一律 404，
//      用户完全无法理解发生了什么。
//
// 修复要求：删除成功后必须关闭该 Agent 的存量连接，
// 且在线表里不能留下任何痕迹。

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"
	"github.com/gorilla/websocket"

	"arkade/hub/internal/agentlink"
	"arkade/protocol/envelope"
)

// TestDeleteAgentClosesLiveConnection 是本修复的核心测试。
func TestDeleteAgentClosesLiveConnection(t *testing.T) {
	const agentToken = "test-agent-token-0123456789"

	// 用与新路由同一套依赖（真库 + 真 Registry）。
	e := newTestEnv(t)
	reg := agentlink.NewRegistry()

	// 构造一个同时含 HTTP 接口与 Agent 连接入口的服务器。
	handler := e.routerWithRegistry(t, reg)
	srv := newTestServer(t, handler)
	_ = srv

	// 1. 让一个 Agent 真正连上来。
	agentID := connectRealAgent(t, srv, agentToken)
	if !reg.IsOnline(agentID) {
		t.Fatal("Agent 连上后应出现在在线表中")
	}

	// 2. 管理员登录并删除该服务器记录。
	//
	// 关键：必须把请求发到**同一条** handler（也就是同一个 Registry），
	// 否则删的是另一个路由里的 Registry，Close 找不到连接，
	// 测试会以一种误导性的方式失败（看起来像功能没做）。
	cookies := e.loginAsFor(t, srv, "admin", testPassword)
	resp, body := doHTTP(t, http.MethodDelete, srv+"/api/agents/"+agentID, "", cookies)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("删除应成功，实际 %d，响应 %s", resp.StatusCode, body)
	}

	// 3. 在线表里必须立刻没有这条连接。
	//
	// 用轮询而不是立即断言：Close 走的是"先发 session.close 再关闭"，
	// 而在线表的移除可能发生在连接读循环退出的那一刻，有一点点延迟。
	waitUntil(t, 3*time.Second, func() bool {
		return !reg.IsOnline(agentID)
	}, "删除服务器记录后，在线表中不应还留着该 Agent 的连接（幽灵在线）")

	// 4. 计数也必须归零 —— 这是 /healthz 上显示给运维的数字。
	if n := reg.Count(); n != 0 {
		t.Errorf("/healthz 的 agents_online 应为 0，实际 %d", n)
	}
}

// TestDeletedAgentCanReRegister 验证删除不是永久拉黑。
//
// 删除只移除记录；Agent 断开后重连会在握手阶段重新注册。
// 若实现把删除当成了拉黑，这条测试会失败——
// 而那种行为会让用户"删掉再接入"的操作彻底失效。
func TestDeletedAgentCanReRegister(t *testing.T) {
	const agentToken = "test-agent-token-0123456789"

	e := newTestEnv(t)
	reg := agentlink.NewRegistry()
	srv := newTestServer(t, e.routerWithRegistry(t, reg))

	agentID := connectRealAgent(t, srv, agentToken)

	cookies := e.loginAsFor(t, srv, "admin", testPassword)
	if resp, _ := doHTTP(t, http.MethodDelete, srv+"/api/agents/"+agentID, "", cookies); resp.StatusCode != http.StatusOK {
		t.Fatalf("删除应成功，实际 %d", resp.StatusCode)
	}
	waitUntil(t, 3*time.Second, func() bool { return !reg.IsOnline(agentID) }, "连接应被关闭")

	// 重连：应能正常握手并重新出现在列表里。
	agentID2 := connectRealAgent(t, srv, agentToken)
	if !reg.IsOnline(agentID2) {
		t.Fatal("重连后应重新出现在在线表中")
	}

	// 记录也应被重新写入。
	if _, err := e.db.GetAgent(t.Context(), agentID2); err != nil {
		t.Errorf("重连后应重新注册记录: %v", err)
	}
}

// TestDeleteOfflineAgentIsSafe 验证删除离线服务器不会 panic 或报错。
//
// Registry.Close 对不在线的 Agent 必须是安全的空操作。
// 删除一台早就掉线的服务器是常见操作，此时不能因为"找不到连接"而失败。
func TestDeleteOfflineAgentIsSafe(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "offline-1", "host-1")

	rec, _ := e.do(t, http.MethodDelete, "/api/agents/offline-1", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除离线服务器应成功，实际 %d，响应 %s", rec.Code, rec.Body.String())
	}
}

// TestDeleteAgentMessageHasNoOrphanDirectionClaim 锁定响应文案的语义正确性。
//
// **两种 orphan 方向相反，不能混为一谈**：
//   - 服务器侧 orphan_stacks：Hub 里的配置记录失去了归属服务器，
//     配置内容仍在服务端；
//   - Agent 侧 orphans：机器上有容器在跑，但 Hub 没有对应记录。
//
// 若文案把前者说成"变成 Agent 的孤儿"，用户会以为配置跑到机器上去了，
// 进而去机器上找配置——那是一次注定失败的排查。
func TestDeleteAgentMessageHasNoOrphanDirectionClaim(t *testing.T) {
	e := newTestEnv(t)
	cookies := e.loginAs(t, "admin", testPassword)
	e.seedAgentRecord(t, "a1", "host-1")
	e.seedStackRecord(t, "a1", "web")

	rec, _ := e.do(t, http.MethodDelete, "/api/agents/a1", "", cookies)
	if rec.Code != http.StatusOK {
		t.Fatalf("删除应成功，实际 %d", rec.Code)
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	msg, _ := resp["message"].(string)
	if msg == "" {
		t.Fatal("响应必须带说明文案")
	}

	// 不能把服务端配置说成机器侧孤儿。
	if strings.Contains(msg, "孤儿") {
		t.Errorf("文案不应使用「孤儿」一词（易与 Agent 侧 orphans 混淆），实际 %q", msg)
	}
	// 必须说明配置仍在服务端 / 重新接入后可用。
	if !strings.Contains(msg, "保留") {
		t.Errorf("文案应说明配置仍保留，实际 %q", msg)
	}
	if !strings.Contains(msg, "重新接入") {
		t.Errorf("文案应说明重新接入后可继续使用，实际 %q", msg)
	}
	// 必须说明机器上的容器未受影响。
	if !strings.Contains(msg, "容器") {
		t.Errorf("文案应说明机器上的容器未受影响，实际 %q", msg)
	}
}

// routerWithRegistry 用指定 Registry 重建一条完整路由。
//
// 为什么需要它：newTestEnv 内部自己建了一个 Registry，而测试要拿到
// 同一个 Registry 的引用来断言在线状态，因此这里用外部传入的实例重建路由。
// 其余依赖（真库、真 auth）沿用 newTestEnv 的，保证行为与生产一致。
//
// 返回 *ghttp.Server：迁移后必须真实 Start 才有完整生命周期
// （见 router_start.go 的说明），因此不再退化成 http.Handler。
func (e *testEnv) routerWithRegistry(t *testing.T, reg *agentlink.Registry) *ghttp.Server {
	t.Helper()
	log := discardLogger()
	agentSrv := agentlink.NewServer(reg, testAgentToken, "test", &agentSink{db: e.db, log: log}, log)
	return NewRouter(Deps{
		Log:        log,
		DB:         e.db,
		Auth:       e.auth,
		Registry:   reg,
		Ops:        agentlink.NewOps(reg),
		AgentConn:  http.HandlerFunc(agentSrv.HandleConnect),
		HubVersion: "test",
	})
}

// testAgentToken 是测试用的 Agent 连接令牌。
const testAgentToken = "test-agent-token-0123456789"

// newTestServer 起一个真实监听的服务并注册清理。
//
// 迁移说明：原来用 httptest.NewServer(h) 包住未启动的 ghttp.Server。
// 那样不可用——ghttp 的 session 生命周期只在 Start() 里建立，
// 未启动时请求收尾会 nil panic（表现为客户端收到 EOF）。
// 现在改为真实 Start + ephemeral 端口。
func newTestServer(t *testing.T, h *ghttp.Server) string {
	t.Helper()
	h.SetAddr("127.0.0.1:0")
	if err := h.Start(); err != nil {
		t.Fatalf("启动测试服务器失败: %v", err)
	}
	t.Cleanup(func() { _ = h.Shutdown() })
	return "http://" + h.GetListenedAddress()
}

// doHTTP 向真实 httptest 服务器发一次请求。
//
// 为什么不用 e.do：e.do 走的是 newTestEnv 自带的 router（另一个 Registry），
// 而本组测试必须打到与 Agent 连接同一个 Registry 的 handler 上。
func doHTTP(t *testing.T, method, url, body string, cookies []*http.Cookie) (*http.Response, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp, raw
}

// loginAsFor 向指定服务器登录并返回 Cookie。
func (e *testEnv) loginAsFor(t *testing.T, srvURL, username, password string) []*http.Cookie {
	t.Helper()
	resp, _ := doHTTP(t, http.MethodPost, srvURL+"/api/auth/login",
		`{"username":"`+username+`","password":"`+password+`"}`, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("账号 %s 登录应成功，实际 %d", username, resp.StatusCode)
	}
	return resp.Cookies()
}

// connectRealAgent 拨号并完成一次真实握手，返回 Hub 分配的 agent_id。
func connectRealAgent(t *testing.T, srvURL, token string) string {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + envelope.AgentConnectPath
	header := http.Header{}
	header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+token)
	header.Set(envelope.HeaderProtoVersion, "1")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("Agent 建连失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	hello, err := envelope.NewRequest("hello-1", envelope.MethodHello, "hub", envelope.HelloPayload{
		AgentVersion: "0.1.0-test", ProtoVersion: 1, Hostname: "del-test-host",
		OS: "linux", Arch: "amd64",
		Runtime: envelope.RuntimeInfo{Kind: envelope.RuntimeDocker, Version: "27.3.1"},
	})
	if err != nil {
		t.Fatalf("构造 hello 失败: %v", err)
	}
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatalf("发送 hello 失败: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack envelope.Envelope
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("读取 HelloAck 失败: %v", err)
	}
	if ack.Error != nil {
		t.Fatalf("握手返回错误: %s", ack.Error.Message)
	}
	var payload envelope.HelloAckPayload
	if err := envelope.DecodePayload(&ack, &payload); err != nil {
		t.Fatalf("解析 HelloAck 失败: %v", err)
	}
	if payload.AgentID == "" {
		t.Fatal("Hub 必须分配 agent_id")
	}
	return payload.AgentID
}

// waitUntil 轮询等待条件成立。
//
// 不用固定 sleep：固定时长要么白等、要么在大负载机器上偶发失败。
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error(msg)
}
