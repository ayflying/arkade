package httpx

// agent_handshake_reject_test.go 覆盖"被禁用的服务器在握手阶段被拒绝"。
//
// 这是本任务要求的关键行为，且必须验证到两点：
//  1. 连接确实被拒（不会进在线表）；
//  2. **拒绝原因真的送到 Agent 手里**——如果只是断开连接，
//     Agent 侧只能看到"连不上"，会当成网络故障无限重连，
//     运维也永远查不到真正原因（"你被禁用了"）。
//
// 因此这里用真实的 WebSocket 拨号，而不是只测 OnAgentHello 的返回值。

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

// newRealSinkRouter 构造使用真实 agentSink（接真实数据库）的测试路由。
//
// 与 newConnectRouter 的区别：那个用 fakeSink 只记录回调，
// 这个要走真实的禁用检查逻辑，因此必须接真库。
func newRealSinkRouter(t *testing.T, db *store.DB, reg *agentlink.Registry, token string) http.Handler {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	sink := &agentSink{db: db, log: log}
	srv := agentlink.NewServer(reg, token, "test", sink, log)
	return NewRouter(Deps{
		Log:        log,
		DB:         db,
		Registry:   reg,
		Ops:        agentlink.NewOps(reg),
		Auth:       auth.NewService(nil, time.Hour),
		AgentConn:  http.HandlerFunc(srv.HandleConnect),
		HubVersion: "test",
	})
}

// dialExpectReject 以 Agent 身份拨号并期待握手被拒，返回收到的错误文案。
func dialExpectReject(t *testing.T, srvURL, token, agentID string) string {
	t.Helper()

	wsURL := "ws" + strings.TrimPrefix(srvURL, "http") + envelope.AgentConnectPath
	header := http.Header{}
	header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+token)
	header.Set(envelope.HeaderProtoVersion, "1")
	// 复用已分配的 ID（模拟"被禁用后重连"的真实场景）。
	if agentID != "" {
		header.Set(envelope.HeaderAgentID, agentID)
	}

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("WebSocket 建连失败（连接层本身应该建立，拒绝发生在握手阶段）: %v", err)
	}
	defer conn.Close()

	hello, err := envelope.NewRequest("hello-1", envelope.MethodHello, "hub", envelope.HelloPayload{
		AgentVersion: "0.1.0-test",
		ProtoVersion: 1,
		Hostname:     "test-host",
		OS:           "linux",
		Arch:         "amd64",
		Runtime:      envelope.RuntimeInfo{Kind: envelope.RuntimeDocker, Version: "27.3.1"},
	})
	if err != nil {
		t.Fatalf("构造 hello 失败: %v", err)
	}
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatalf("发送 hello 失败: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var resp envelope.Envelope
	if err := conn.ReadJSON(&resp); err != nil {
		t.Fatalf("应收到拒绝原因而不是直接断开（否则 Agent 无从得知为何连不上）: %v", err)
	}
	if resp.Error == nil {
		t.Fatalf("握手应被拒绝，实际收到成功响应: %+v", resp)
	}
	return resp.Error.Message
}

// TestDisabledAgentRejectedAtHandshake 验证被禁用的 Agent 握手被拒且原因明确。
func TestDisabledAgentRejectedAtHandshake(t *testing.T) {
	const agentToken = "test-agent-token-0123456789"

	env := newTestEnv(t)
	// 先在库里放一台"已禁用"的 Agent。
	const agentID = "disabled-agent-1"
	env.seedAgentRecord(t, agentID, "host-1")
	if err := env.db.SetAgentDisabled(t.Context(), agentID, true, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	reg := agentlink.NewRegistry()
	srv := httptest.NewServer(newRealSinkRouter(t, env.db, reg, agentToken))
	defer srv.Close()

	msg := dialExpectReject(t, srv.URL, agentToken, agentID)

	// 关键断言 1：文案必须能让人看懂该怎么办。
	if !strings.Contains(msg, "禁用") {
		t.Errorf("拒绝原因应说明「被禁用」，实际 %q", msg)
	}
	if !strings.Contains(msg, "管理员") {
		t.Errorf("拒绝原因应指引联系管理员，实际 %q", msg)
	}

	// 关键断言 2：被拒的连接绝不能进在线表。
	// 否则界面上会显示一台"已禁用但在线"的服务器，状态自相矛盾。
	if reg.IsOnline(agentID) {
		t.Error("被禁用的 Agent 不应出现在在线表中")
	}
}

// TestDisabledAgentRejectDoesNotTouchLastSeen 验证被拒的握手不刷新记录。
//
// 如果实现是先写库再检查，被禁用的机器每次重连都会刷新 last_seen 与
// status=online，界面上一台"已禁用"的机器却显示刚活跃过，
// 运维无法判断禁用到底生效没有。
func TestDisabledAgentRejectDoesNotTouchLastSeen(t *testing.T) {
	const agentToken = "test-agent-token-0123456789"

	env := newTestEnv(t)
	const agentID = "disabled-agent-2"
	env.seedAgentRecord(t, agentID, "host-1")
	if err := env.db.SetAgentDisabled(t.Context(), agentID, true, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	before, err := env.db.GetAgent(t.Context(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	// 把 last_seen 改成很久以前，验证重连不会把它刷新。
	oldSeen := int64(1000)
	if err := env.db.UpdateAgentStatus(t.Context(), agentID, "offline", oldSeen); err != nil {
		t.Fatal(err)
	}

	reg := agentlink.NewRegistry()
	srv := httptest.NewServer(newRealSinkRouter(t, env.db, reg, agentToken))
	defer srv.Close()

	_ = dialExpectReject(t, srv.URL, agentToken, agentID)

	after, err := env.db.GetAgent(t.Context(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastSeen != oldSeen {
		t.Errorf("被拒的握手不应刷新 last_seen（原 %d，现 %d）", oldSeen, after.LastSeen)
	}
	if after.Status == "online" {
		t.Error("被拒的握手不应把状态写成 online")
	}
	// 而它当然仍应保持禁用。
	if !after.Disabled {
		t.Error("禁用标记不应被清除")
	}
	_ = before
}

// TestEnabledAgentHandshakeSucceeds 验证启用后能正常握手。
//
// 这是"禁用可逆"的实证：只测拒绝不测恢复的话，
// 万一实现把禁用写成了永久拉黑，测试也发现不了。
func TestEnabledAgentHandshakeSucceeds(t *testing.T) {
	const agentToken = "test-agent-token-0123456789"

	env := newTestEnv(t)
	const agentID = "reenable-agent-1"
	env.seedAgentRecord(t, agentID, "host-1")
	if err := env.db.SetAgentDisabled(t.Context(), agentID, true, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	reg := agentlink.NewRegistry()
	srv := httptest.NewServer(newRealSinkRouter(t, env.db, reg, agentToken))
	defer srv.Close()

	// 禁用时被拒。
	_ = dialExpectReject(t, srv.URL, agentToken, agentID)

	// 启用后应能正常握手。
	if err := env.db.SetAgentDisabled(t.Context(), agentID, false, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + envelope.AgentConnectPath
	header := http.Header{}
	header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+agentToken)
	header.Set(envelope.HeaderProtoVersion, "1")
	header.Set(envelope.HeaderAgentID, agentID)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("启用后应能建连: %v", err)
	}
	defer conn.Close()

	hello, _ := envelope.NewRequest("hello-2", envelope.MethodHello, "hub", envelope.HelloPayload{
		AgentVersion: "0.1.0-test", ProtoVersion: 1, Hostname: "test-host",
		Runtime: envelope.RuntimeInfo{Kind: envelope.RuntimeDocker},
	})
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack envelope.Envelope
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("启用后握手应成功: %v", err)
	}
	if ack.Error != nil {
		t.Fatalf("启用后握手不应报错: %s", ack.Error.Message)
	}
}

// TestNewAgentHandshakeAllowed 验证库里没有记录的 Agent 能正常首次注册。
//
// IsAgentDisabled 对不存在的 Agent 返回 ErrNotFound，
// 如果实现把它当成错误处理，新机器就永远连不上了——这是最容易写错的边界。
func TestNewAgentHandshakeAllowed(t *testing.T) {
	const agentToken = "test-agent-token-0123456789"

	env := newTestEnv(t)
	reg := agentlink.NewRegistry()
	srv := httptest.NewServer(newRealSinkRouter(t, env.db, reg, agentToken))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + envelope.AgentConnectPath
	header := http.Header{}
	header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+agentToken)
	header.Set(envelope.HeaderProtoVersion, "1")

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatalf("建连失败: %v", err)
	}
	defer conn.Close()

	hello, _ := envelope.NewRequest("hello-3", envelope.MethodHello, "hub", envelope.HelloPayload{
		AgentVersion: "0.1.0-test", ProtoVersion: 1, Hostname: "brand-new-host",
		Runtime: envelope.RuntimeInfo{Kind: envelope.RuntimeDocker},
	})
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack envelope.Envelope
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("首次注册的机器必须能连上（ErrNotFound 不是错误）: %v", err)
	}
	if ack.Error != nil {
		t.Fatalf("首次注册不应被拒: %s", ack.Error.Message)
	}
}

// TestHandshakeKeepsCustomDisplayName 验证重连不会覆盖用户改过的显示名。
//
// 这是很容易踩的坑：握手会用上报的 Hostname 覆盖 name 列。
// 如果实现每次都直接覆盖，用户改的名字会在下次重连时凭空消失。
func TestHandshakeKeepsCustomDisplayName(t *testing.T) {
	const agentToken = "test-agent-token-0123456789"

	env := newTestEnv(t)
	const agentID = "rename-agent-1"
	env.seedAgentRecord(t, agentID, "host-1")

	// 用户把显示名改成了「生产-东京」。
	name := "生产-东京"
	if _, err := env.db.UpdateAgentAdmin(t.Context(), agentID,
		store.AgentAdminFields{DisplayName: &name}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	reg := agentlink.NewRegistry()
	srv := httptest.NewServer(newRealSinkRouter(t, env.db, reg, agentToken))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + envelope.AgentConnectPath
	header := http.Header{}
	header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+agentToken)
	header.Set(envelope.HeaderProtoVersion, "1")
	header.Set(envelope.HeaderAgentID, agentID)

	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Agent 上报的 Hostname 是 "test-host"，不应覆盖用户改的名字。
	hello, _ := envelope.NewRequest("hello-4", envelope.MethodHello, "hub", envelope.HelloPayload{
		AgentVersion: "0.1.0-test", ProtoVersion: 1, Hostname: "test-host",
		Runtime: envelope.RuntimeInfo{Kind: envelope.RuntimeDocker},
	})
	if err := conn.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack envelope.Envelope
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatal(err)
	}

	rec, err := env.db.GetAgent(t.Context(), agentID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.DisplayName != name {
		t.Errorf("用户改的显示名应保留，实际 %q", rec.DisplayName)
	}
	// 主机名也不应被 test-host 覆盖（原有记录优先）。
	if rec.Name != "host-1" {
		t.Errorf("已有主机名应保留，实际 %q", rec.Name)
	}
}

// TestDisabledMessageIsActionable 验证拒绝文案包含可操作信息。
//
// 文案是给机器旁的运维看的，必须能直接指引下一步动作，
// 不能是 "forbidden" 这类无信息量的词。
func TestDisabledMessageIsActionable(t *testing.T) {
	msg := DisabledAgentMessage
	if !strings.Contains(msg, "禁用") {
		t.Error("文案应说明被禁用")
	}
	if !strings.Contains(msg, "管理员") {
		t.Error("文案应指引联系管理员")
	}
	if !strings.Contains(msg, "启用") {
		t.Error("文案应说明如何恢复（启用）")
	}
	// 不应是纯英文。
	if strings.Contains(msg, "forbidden") {
		t.Errorf("不应直接把错误码当文案，实际 %q", msg)
	}
}

// errHandshakeRejected 仅用于让 fmt 导入不被优化掉的占位错误。
var errHandshakeRejected = errors.New("握手被拒绝")

var _ = fmt.Sprintf
