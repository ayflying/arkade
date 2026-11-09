package httpx

// enroll_helpers_test.go 提供预创建服务器测试的接线工具。
//
// 关键原则：**必须复用 newTestEnv 里那一套真实装配**。
//
// 早先这里另建 Registry 与 httptest 服务，结果是：
// HTTP 的"重置密钥"踢的是 A 在线表，而测试的 Agent 连在 B 在线表上。
// 那会让正确的生产代码测不过，更糟的是会掩盖真实的"重置没踢连接"bug。
// 现在 e.wsURL / e.registry / e.guard 就是生产装配的同一份对象。

import (
	"net/http"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"arkade/hub/internal/agentlink"
	"arkade/protocol/envelope"
)

// startEnrollAgent 返回测试环境共用的在线表与守卫（与 e.do 的 API 是同一套）。
//
// 保留"返回三个值"的形状只是为了少改调用点；它不再新建任何服务。
func startEnrollAgent(t *testing.T, e *testEnv) (string, *agentlink.Registry, *agentlink.EnrollmentGuard) {
	t.Helper()
	return e.wsURL, e.registry, e.guard
}

// newTestGuard 构造使用默认超时的守卫。
func newTestGuard() *agentlink.EnrollmentGuard {
	return agentlink.NewEnrollmentGuard()
}

// newFastTimeoutGuard 构造短超时守卫，用于测"超时失败而不是挂起"。
func newFastTimeoutGuard() *agentlink.EnrollmentGuard {
	return agentlink.NewEnrollmentGuardWithTimeout(300 * time.Millisecond)
}

// dialWith 用给定密钥建连并等待 HelloAck，返回连接与 ack 载荷。
//
// headerID 非空时写入 X-Arkade-Agent-Id，用于验证"Header 里的 ID 不被采纳"。
func dialWith(t *testing.T, srvURL, token, headerID, hostname string) (*websocket.Conn, envelope.HelloAckPayload) {
	t.Helper()
	wsURL := "ws" + srvURL[len("http"):] + envelope.AgentConnectPath
	h := http.Header{}
	h.Set(envelope.HeaderAuthorization, "Bearer "+token)
	if headerID != "" {
		h.Set(envelope.HeaderAgentID, headerID)
	}
	h.Set(envelope.HeaderProtoVersion, "1")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, h)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}

	hello, err := envelope.NewRequest("h-1", envelope.MethodHello, "agent", envelope.HelloPayload{
		AgentVersion: "0.1.0-test", ProtoVersion: 1, Hostname: hostname,
		OS: "linux", Arch: "amd64",
		Runtime: envelope.RuntimeInfo{Kind: envelope.RuntimeDocker, Version: "27.3.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(hello); err != nil {
		_ = conn.Close()
		t.Fatalf("发送 hello 失败: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack envelope.Envelope
	if err := conn.ReadJSON(&ack); err != nil {
		_ = conn.Close()
		t.Fatalf("读取 HelloAck 失败: %v", err)
	}
	var payload envelope.HelloAckPayload
	if err := envelope.DecodePayload(&ack, &payload); err != nil {
		_ = conn.Close()
		t.Fatalf("解析 HelloAck 失败: %v", err)
	}
	// 握手完成后取消读超时，否则连接会在 5 秒后被判超时。
	_ = conn.SetReadDeadline(time.Time{})
	return conn, payload
}

// dialOnly 只完成 WebSocket 升级（此时 authorize 已通过），不发送 hello。
//
// 这正是漏洞的时间窗：认证已过，但握手还没走到临界区。
// 真实世界里它也经常出现——网络慢、客户端启动慢、进程被调度挂起，
// 都会让"已建连、未发 hello"持续几十毫秒到几秒。
func dialOnly(t *testing.T, srvURL, token string) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + srvURL[len("http"):] + envelope.AgentConnectPath
	h := http.Header{}
	h.Set(envelope.HeaderAuthorization, "Bearer "+token)
	h.Set(envelope.HeaderProtoVersion, "1")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, h)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}
	return conn
}

// sendHello 在已建立的连接上发送 hello 并读取应答。
//
// 返回的 Envelope 里 Error 非空表示被拒。
// err 非空表示连接直接断了（没有礼貌的拒绝原因）——也算拒绝。
func sendHello(t *testing.T, conn *websocket.Conn) (envelope.Envelope, error) {
	t.Helper()
	hello, err := envelope.NewRequest("h-1", envelope.MethodHello, "agent", envelope.HelloPayload{
		AgentVersion: "0.1.0-test", ProtoVersion: 1, Hostname: "race-host",
		OS: "linux", Arch: "amd64",
		Runtime: envelope.RuntimeInfo{Kind: envelope.RuntimeDocker, Version: "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(hello); err != nil {
		return envelope.Envelope{}, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var resp envelope.Envelope
	err = conn.ReadJSON(&resp)
	return resp, err
}

// expectHandshakeRejected 断言握手被拒，覆盖两种拒绝形态：
//
//   - 升级阶段就被拒（HTTP 401）→ Dial 返回 bad handshake；
//   - 升级成功、在握手阶段被拒 → 收到带 Error 的信封。
//
// 两种都是合法拒绝。只看其中一种会把"拒绝方式变了"误判成"没拒绝"，
// 也会把真正的放行当成通过。
func expectHandshakeRejected(t *testing.T, srvURL, token string) {
	t.Helper()
	wsURL := "ws" + srvURL[len("http"):] + envelope.AgentConnectPath
	h := http.Header{}
	h.Set(envelope.HeaderAuthorization, "Bearer "+token)
	h.Set(envelope.HeaderProtoVersion, "1")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, h)
	if err != nil {
		// 连接层拒绝：合法。
		return
	}
	defer func() { _ = conn.Close() }()

	resp, err := sendHello(t, conn)
	if err != nil {
		// 直接断开：也算拒绝（但正常的 Hub 会给原因，见下）。
		return
	}
	if resp.Error == nil {
		t.Fatalf("握手应被拒绝，实际收到成功响应 %+v", resp)
	}
}

// dialHelloWithHeaders 建连并完成一次 hello，返回应答（可能带 Error）。
//
// 与 dialWith 的区别：它**不把拒绝当失败**，用于断言"应被拒"的场景。
// headerID 非空时写入 X-Arkade-Agent-Id。
func dialHelloWithHeaders(t *testing.T, srvURL, token, headerID string) (envelope.Envelope, error) {
	t.Helper()
	wsURL := "ws" + srvURL[len("http"):] + envelope.AgentConnectPath
	h := http.Header{}
	h.Set(envelope.HeaderAuthorization, "Bearer "+token)
	h.Set(envelope.HeaderProtoVersion, "1")
	if headerID != "" {
		h.Set(envelope.HeaderAgentID, headerID)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, h)
	if err != nil {
		// 升级阶段被拒（401）：也算一种拒绝形态。
		return envelope.Envelope{}, err
	}
	defer func() { _ = conn.Close() }()
	return sendHello(t, conn)
}

// hashEnrollmentTokenForTest 复用生产的摘要算法。
//
// 刻意调生产函数而不是再写一遍 sha256：
// 两处实现若有一天分叉（比如加了盐），测试仍会通过，
// 而真实认证会全部失败——那种 bug 极难定位。
func hashEnrollmentTokenForTest(token string) string {
	return agentlink.HashEnrollmentToken(token)
}
