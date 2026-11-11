package httpx

// agentconn_test.go 覆盖 Agent 反向连接的真实 WebSocket 握手与请求往返。
//
// 为什么必须有这个测试：曾经出现过真实缺陷——日志中间件的 ResponseWriter 包装器
// 没有透传 http.Hijacker，导致 WebSocket 升级稳定失败并返回 500，
// 而当时所有单元测试都通过（因为没有一个测试真的把 Agent 连上来）。
// agentconn_test.go 用真实 WebSocket 验证 Agent 建连与握手。
//
// 所有用例都必须走原生 Start() 的 GF 服务器：未 Start 时
// gsession 为 nil，请求收尾会 panic（Linux 上可能被吞但日志仍在）。
// 这个测试用真实的 GF 服务 + gorilla 拨号器，是该缺陷的回归防线。
//
// 模拟方式说明：测试里的"Agent"刻意按真实 Agent 的会话语义实现——
// 收到 request 就执行处理并回 response（与 agent/internal/handler.Registry.Dispatch 一致），
// 而不是自己发明一套消息方向。这样才能验证协议是真的对齐了。

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/auth"
	"arkade/protocol/envelope"
)

// fakeSink 记录业务层收到的回调，用于断言 Hub 侧处理结果。
type fakeSink struct {
	mu         sync.Mutex
	helloIDs   []string
	heartbeats int
	snapshots  int
	offline    []string
}

func (f *fakeSink) OnAgentHello(ctx context.Context, agentID string, info envelope.HelloPayload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.helloIDs = append(f.helloIDs, agentID)
	return nil
}

func (f *fakeSink) OnAgentHeartbeat(ctx context.Context, info envelope.HeartbeatPayload) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.heartbeats++
}

func (f *fakeSink) OnAgentSnapshot(ctx context.Context, agentID string, containers []envelope.ContainerSummary) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshots++
}

func (f *fakeSink) OnAgentDisconnected(ctx context.Context, agentID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.offline = append(f.offline, agentID)
}

// fakeAgentClient 模拟真实 Agent 的会话行为：
// 建连 → 发 hello → 读 HelloAck → 对收到的每个 request 执行处理并回 response。
type fakeAgentClient struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	methods []string // 收到过的方法名，用于断言
}

// dialAsAgent 建立一条真实的 Agent 连接并完成握手。
func dialAsAgent(t *testing.T, srvURL, token string) (*fakeAgentClient, envelope.HelloAckPayload) {
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
	var ack envelope.Envelope
	if err := conn.ReadJSON(&ack); err != nil {
		t.Fatalf("读取 HelloAck 失败: %v", err)
	}
	if ack.Type != envelope.TypeResponse {
		t.Fatalf("HelloAck 应为 response 类型，实际 %s", ack.Type)
	}
	if ack.Error != nil {
		t.Fatalf("握手返回错误: %s", ack.Error.Message)
	}

	var ackPayload envelope.HelloAckPayload
	if err := envelope.DecodePayload(&ack, &ackPayload); err != nil {
		t.Fatalf("解析 HelloAck 失败: %v", err)
	}
	return &fakeAgentClient{conn: conn}, ackPayload
}

// serveRequests 按真实 Agent 的语义处理下行请求，直到连接关闭。
func (c *fakeAgentClient) serveRequests(t *testing.T, results map[string]any) {
	go func() {
		for {
			var env envelope.Envelope
			if err := c.conn.ReadJSON(&env); err != nil {
				return
			}
			if env.Type != envelope.TypeRequest {
				continue
			}
			c.mu.Lock()
			c.methods = append(c.methods, env.Method)
			c.mu.Unlock()

			result, ok := results[env.Method]
			if !ok {
				resp := envelope.NewErrorResponse(&env,
					envelope.NewError(envelope.CodeUnsupported, "Agent 不存在该方法"))
				_ = c.conn.WriteJSON(resp)
				continue
			}
			resp, err := envelope.NewResponse(&env, result)
			if err != nil {
				continue
			}
			_ = c.conn.WriteJSON(resp)
		}
	}()
}

// agentTestSrv 是 Agent 连接测试用的原生服务实例。
//
// 必须用真实 Start() 的 *ghttp.Server，不能用 httptest.NewServer 包一层：
// ghttp 的完整生命周期（含 sessionManager）由 Start() 建立，
// 未 Start 直接 ServeHTTP 会在请求收尾时因 gsession 为 nil 而 panic。
// 这个 panic 在 Linux 上可能被 recover 吞掉（exit 0），日志里却真实存在。
type agentTestSrv struct {
	url     string
	tracker *connectionTracker
	reg     *agentlink.Registry
}

// close 关闭所有 Agent 连接并 join service 端 handler，再停服务器。
//
// WebSocket 是 hijack 的连接，http.Server.Shutdown 不会等它；
// 不 join 就结束会让 handler 在库关闭后继续写。
func (s *agentTestSrv) close(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.tracker.close(ctx, s.reg); err != nil {
		t.Errorf("等待 Agent handler 退出超时: %v", err)
	}
}

// startConnectRouter 用真实 Start() 启动一条只挂 Agent 连接入口的路由。
//
// 关键点：必须经过 NewRouter 的真实中间件链，否则复现不了
// Hijacker 被遮断的问题。
func startConnectRouter(t *testing.T, reg *agentlink.Registry, sink agentlink.HelloHandler, token string) *agentTestSrv {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	agentSrv := agentlink.NewServer(reg, token, "test", sink, log)
	tracker := &connectionTracker{}
	router := NewRouter(Deps{
		Log:        log,
		Registry:   reg,
		Ops:        agentlink.NewOps(reg),
		Auth:       auth.NewService(nil, time.Hour),
		AgentConn:  tracker.wrap(http.HandlerFunc(agentSrv.HandleConnect)),
		HubVersion: "test",
	})
	h := &agentTestSrv{reg: reg, tracker: tracker}
	h.url = startRouterForTest(t, router)
	t.Cleanup(func() { h.close(t) })
	return h
}

// TestAgentConnectHandshake 验证：真实 WebSocket 能连上、握手成功、拿到 agent_id、
// 心跳事件能到达业务层、下行容器请求能经连接往返、断开后正确触发离线。
func TestAgentConnectHandshake(t *testing.T) {
	const agentToken = "test-agent-token-0123456789"

	reg := agentlink.NewRegistry()
	sink := &fakeSink{}
	srv := startConnectRouter(t, reg, sink, agentToken)

	client, ack := dialAsAgent(t, srv.url, agentToken)

	// --- 1. Hub 必须分配 agent_id 与正数心跳间隔 ---
	if ack.AgentID == "" {
		t.Fatal("Hub 必须分配 agent_id")
	}
	if ack.HeartbeatSec <= 0 {
		t.Errorf("Hub 必须下发正数 heartbeat_sec，实际 %d", ack.HeartbeatSec)
	}

	// --- 2. 业务层应收到 hello 回调，在线表应登记该 Agent ---
	waitFor(t, 2*time.Second, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return len(sink.helloIDs) == 1 && sink.helloIDs[0] == ack.AgentID
	}, "握手回调应收到正确的 agent_id")

	if _, ok := reg.Get(ack.AgentID); !ok {
		t.Fatal("在线表中应存在该 Agent")
	}

	// --- 3. 下行容器列表请求，应能拿到对端响应 ---
	client.serveRequests(t, map[string]any{
		envelope.MethodContainerList: envelope.ContainerListResult{
			AgentID: ack.AgentID,
			Containers: []envelope.ContainerSummary{
				{ID: "abc123def456", Name: "nginx", Image: "nginx:1.27", State: "running", Origin: "arkade"},
			},
		},
	})

	res, cerr := agentlink.NewOps(reg).ListContainers(context.Background(), ack.AgentID)
	if cerr != nil {
		t.Fatalf("下发容器列表失败: %s (%s)", cerr.Message, cerr.Code)
	}
	if len(res.Containers) != 1 || res.Containers[0].Name != "nginx" {
		t.Fatalf("返回的容器列表不符合预期: %+v", res.Containers)
	}

	// --- 4. 发送心跳事件，业务层应收到 ---
	hb, _ := envelope.NewEvent("hb-1", envelope.MethodHeartbeat, "hub", envelope.HeartbeatPayload{
		AgentID: "should-be-overridden-by-hub", At: time.Now().Unix(),
	})
	if err := client.conn.WriteJSON(hb); err != nil {
		t.Fatalf("发送心跳失败: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return sink.heartbeats > 0
	}, "业务层应收到心跳事件")

	// --- 5. 断开后在线表移除、触发离线回调 ---
	_ = client.conn.Close()
	waitFor(t, 3*time.Second, func() bool {
		_, stillOnline := reg.Get(ack.AgentID)
		sink.mu.Lock()
		defer sink.mu.Unlock()
		return !stillOnline && len(sink.offline) > 0
	}, "断开后应移除在线记录并回调 OnAgentDisconnected")
}

// TestAgentConnectRejectsWrongToken 验证 Token 校验：错误 Token 与查询串 Token 都不放行。
func TestAgentConnectRejectsWrongToken(t *testing.T) {
	const agentToken = "correct-token-0123456789"

	reg := agentlink.NewRegistry()
	srv := startConnectRouter(t, reg, &fakeSink{}, agentToken)

	wsURL := "ws" + strings.TrimPrefix(srv.url, "http") + envelope.AgentConnectPath

	// 错误 Token
	header := http.Header{}
	header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+"wrong-token")
	if _, _, err := websocket.DefaultDialer.Dial(wsURL, header); err == nil {
		t.Fatal("错误 Token 必须被拒绝")
	}

	// 缺少 Authorization 头
	if _, _, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil {
		t.Fatal("缺少 Authorization 头必须被拒绝")
	}

	// Token 放在查询串里也必须被拒绝：查询串会被代理与访问日志留存，
	// 不能作为凭据通道。
	if _, _, err := websocket.DefaultDialer.Dial(wsURL+"?token="+agentToken, nil); err == nil {
		t.Fatal("查询串中的 Token 必须被忽略")
	}
}

// TestOfflineAgentRejectedImmediately 验证 Agent 离线时请求立刻失败，
// 而不是挂起等待超时。这是 02-features.md 第 1 节的硬性要求。
func TestOfflineAgentRejectedImmediately(t *testing.T) {
	reg := agentlink.NewRegistry()
	ops := agentlink.NewOps(reg)

	start := time.Now()
	_, cerr := ops.ListContainers(context.Background(), "not-online")
	elapsed := time.Since(start)

	if cerr == nil {
		t.Fatal("对离线 Agent 的请求必须失败")
	}
	if cerr.Code != envelope.CodeAgentOffline {
		t.Errorf("错误码应为 agent_offline，实际 %q", cerr.Code)
	}
	// 必须快速失败：留 2 秒余量给慢 CI，正常应在毫秒级返回。
	if elapsed > 2*time.Second {
		t.Errorf("离线拒绝应立即返回，实际耗时 %v", elapsed)
	}
}

// waitFor 轮询等待条件成立，超时则测试失败并输出说明。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", desc)
}
