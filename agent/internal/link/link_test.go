package link

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// quietLogger 让测试输出不被日志淹没。
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// 测试专用：把过长的常量缩短，避免单测真的等 60 秒。
const (
	testHeartbeatSec = 1
)

// testRuntime 是极简运行时，只用于给 hello/heartbeat 提供数据。
type testRuntime struct{}

func (testRuntime) Info(context.Context) (runtime.Info, error) {
	return runtime.Info{Kind: runtime.KindDocker, Version: "27.3.1", APIVer: "1.47", Endpoint: "unix:///var/run/docker.sock"}, nil
}

func (testRuntime) List(context.Context, bool) ([]runtime.Container, error) {
	return []runtime.Container{{ID: "abc", Name: "web", State: "running"}}, nil
}

func (testRuntime) Start(context.Context, string) error        { return nil }
func (testRuntime) Stop(context.Context, string, bool) error   { return nil }
func (testRuntime) Remove(context.Context, string, bool) error { return nil }
func (testRuntime) Logs(context.Context, string, runtime.LogOptions) (runtime.LogResult, error) {
	return runtime.LogResult{Lines: []string{"hello"}}, nil
}

// echoDispatcher 是对 Hub 请求的处理：把方法名回显，用于验证路由链路通畅。
func echoDispatcher(_ context.Context, req *envelope.Envelope) *envelope.Envelope {
	resp, err := envelope.NewResponse(req, map[string]string{"echo": req.Method})
	if err != nil {
		return envelope.NewErrorResponse(req, envelope.NewError(envelope.CodeInternal, err.Error()))
	}
	return resp
}

// upgrader 允许测试客户端的任意 Origin（Agent 不是浏览器）。
var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

// TestSnapshotCarriesAssignedAgentID 回归测试：
//
// 握手成功后才上报快照，此时 agent_id 已经是 Hub 下发的值。
// 曾经出现过"快照里 agent_id 为空"的 bug——因为快照载荷直接照抄了本地处理器的
// 返回值，而处理器并不知道连接身份。现在由连接层统一盖章，这里固定住该行为。
func TestSnapshotCarriesAssignedAgentID(t *testing.T) {
	var snapshotAgentID atomic.Value
	done := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		var hello envelope.Envelope
		if err := c.ReadJSON(&hello); err != nil {
			return
		}
		ack, _ := envelope.NewResponse(&hello, envelope.HelloAckPayload{AgentID: "assigned-9", HeartbeatSec: 1})
		_ = c.WriteJSON(ack)

		for {
			var env envelope.Envelope
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type == envelope.TypeEvent && env.Method == envelope.MethodSnapshotPush {
				var res envelope.ContainerListResult
				_ = envelope.DecodePayload(&env, &res)
				snapshotAgentID.Store(res.AgentID)
				close(done)
				return
			}
		}
	}))
	defer srv.Close()

	agent, err := New(Options{
		HubURL:       "ws" + srv.URL[len("http"):],
		Token:        "tok",
		AgentVersion: "0.1.0",
		HeartbeatSec: 1,
		Logger:       quietLogger(),
		Runtime:      testRuntime{},
		// 这里的处理器返回的 agent_id 故意留空，模拟"本地处理器不知道身份"。
		Dispatch: func(_ context.Context, req *envelope.Envelope) *envelope.Envelope {
			resp, _ := envelope.NewResponse(req, envelope.ContainerListResult{Containers: nil})
			return resp
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go func() { _ = agent.Run(ctx) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("超时：未收到快照")
	}
	if got, _ := snapshotAgentID.Load().(string); got != "assigned-9" {
		t.Fatalf("快照 agent_id = %q，期望 assigned-9（连接层必须补齐身份）", got)
	}
}

// TestBackoffSequence 验证退避序列：1s → 2s → 4s … 上限 60s。
func TestBackoffSequence(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want time.Duration
	}{
		{0, MinBackoff}, // 未初始化时从最小值开始
		{time.Second, 2 * time.Second},
		{2 * time.Second, 4 * time.Second},
		{32 * time.Second, 60 * time.Second},  // 64s 超限，夹到上限
		{60 * time.Second, 60 * time.Second},  // 已达上限保持不变
		{120 * time.Second, 60 * time.Second}, // 异常大值也要被夹到上限
	}
	for _, c := range cases {
		if got := nextBackoff(c.in); got != c.want {
			t.Fatalf("nextBackoff(%v) = %v，期望 %v", c.in, got, c.want)
		}
	}
	// 连续调用必须严格按 1→2→4→8→16→32→60 递增。
	cur := MinBackoff
	want := []time.Duration{2, 4, 8, 16, 32, 60, 60}
	for i, w := range want {
		cur = nextBackoff(cur)
		if cur != w*time.Second {
			t.Fatalf("第 %d 次退避 = %v，期望 %v 秒", i+1, cur, w)
		}
	}
}

// TestDialURLNormalization 验证 Hub 地址归一化（http→ws、补默认路径）。
func TestDialURLNormalization(t *testing.T) {
	cases := map[string]string{
		"http://hub.example.com":            "ws://hub.example.com/agent/connect",
		"https://hub.example.com":           "wss://hub.example.com/agent/connect",
		"ws://hub.example.com/":             "ws://hub.example.com/agent/connect",
		"wss://hub.example.com:8443/custom": "wss://hub.example.com:8443/custom",
	}
	for in, want := range cases {
		d, err := newDialer(in, "tok", "", "1.0", envelope.Version, false)
		if err != nil {
			t.Fatalf("newDialer(%q) 失败: %v", in, err)
		}
		u := d.u.String()
		if u != want {
			t.Fatalf("newDialer(%q) = %q，期望 %q", in, u, want)
		}
	}

	// 非法输入必须报错而不是静默连一个奇怪的地址。
	for _, bad := range []string{"", "ftp://x", "ws://"} {
		if _, err := newDialer(bad, "t", "", "1", 1, false); err == nil {
			t.Fatalf("newDialer(%q) 应报错", bad)
		}
	}
}

// TestDialHeaders 验证认证与身份头。
func TestDialHeaders(t *testing.T) {
	d, err := newDialer("ws://hub/agent/connect", "secret-token", "agent-1", "0.1.0", envelope.Version, false)
	if err != nil {
		t.Fatal(err)
	}
	h := d.headers()
	if got := h.Get(headerAuth); got != "Bearer secret-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := h.Get(headerAgentID); got != "agent-1" {
		t.Fatalf("X-Arkade-Agent-Id = %q", got)
	}
	if got := h.Get(headerProto); got != "1" {
		t.Fatalf("X-Arkade-Proto = %q", got)
	}
	if got := h.Get(headerUserAgent); got != "arkade-agent/0.1.0" {
		t.Fatalf("User-Agent = %q", got)
	}
}

// TestSessionRequestRouting 验证"收到请求 → 分发 → 回送响应"的完整链路。
//
// 这是 link 包最核心的行为：Hub 下发的 container.list 必须被原样路由到注入的
// Dispatcher，并把响应写回同一条连接。
func TestSessionRequestRouting(t *testing.T) {
	var gotReply atomic.Bool
	hubDone := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		// 读 hello 并回 ack。
		var hello envelope.Envelope
		if err := c.ReadJSON(&hello); err != nil {
			return
		}
		if hello.Method != envelope.MethodHello {
			t.Errorf("首条消息应为 agent.hello，实际 %s", hello.Method)
		}
		ack, _ := envelope.NewResponse(&hello, envelope.HelloAckPayload{
			AgentID:      "hub-assigned-1",
			HubVersion:   "0.1.0",
			HeartbeatSec: 1,
		})
		_ = c.WriteJSON(ack)

		// 读快照事件 + 后续消息，直到收到一条 response（说明请求被处理并回送）。
		for {
			var env envelope.Envelope
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type == envelope.TypeResponse && env.ReplyTo != "" && env.Method == envelope.MethodRuntimeInfo {
				gotReply.Store(true)
				close(hubDone)
				return
			}
			if env.Type == envelope.TypeRequest && env.Method == envelope.MethodHello {
				continue
			}
			// 首次读取快照事件后，向 Agent 下发一个请求。
			if env.Type == envelope.TypeEvent && env.Method == envelope.MethodSnapshotPush {
				req, _ := envelope.NewRequest("req-1", envelope.MethodRuntimeInfo, "agent:"+hello.Method, nil)
				_ = c.WriteJSON(req)
			}
		}
	}))
	defer srv.Close()

	agent, err := New(Options{
		HubURL:       "ws" + srv.URL[len("http"):],
		Token:        "tok",
		DataDir:      t.TempDir(),
		AgentVersion: "0.1.0",
		HeartbeatSec: testHeartbeatSec,
		Logger:       quietLogger(),
		Runtime:      testRuntime{},
		Dispatch:     echoDispatcher,
		Capabilities: func() []string { return []string{envelope.MethodRuntimeInfo} },
	})
	if err != nil {
		t.Fatalf("New 失败: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = agent.Run(ctx) }()

	select {
	case <-hubDone:
	case <-time.After(8 * time.Second):
		t.Fatal("超时：Hub 未收到 Agent 的响应，说明请求路由或回送链路断了")
	}
	if !gotReply.Load() {
		t.Fatal("Agent 未回送 response")
	}
	// Hub 在 HelloAck 里下发的 agent_id 必须被持久化，供下次启动复用。
	if id := agent.AgentID(); id != "hub-assigned-1" {
		t.Fatalf("AgentID = %q，期望 hub-assigned-1", id)
	}
}

// TestHeartbeatSent 验证心跳按 Hub 指定的间隔发出。
func TestHeartbeatSent(t *testing.T) {
	var heartbeats atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()

		var hello envelope.Envelope
		if err := c.ReadJSON(&hello); err != nil {
			return
		}
		var ackPayload envelope.HelloAckPayload
		_ = envelope.DecodePayload(&hello, &ackPayload)
		ack, _ := envelope.NewResponse(&hello, envelope.HelloAckPayload{
			AgentID: "a1", HeartbeatSec: 1, // 1 秒一次，测试才跑得快
		})
		_ = c.WriteJSON(ack)

		for {
			var env envelope.Envelope
			if err := c.ReadJSON(&env); err != nil {
				return
			}
			if env.Type == envelope.TypeEvent && env.Method == envelope.MethodHeartbeat {
				var hb envelope.HeartbeatPayload
				_ = envelope.DecodePayload(&env, &hb)
				if hb.ContainerTotal != 1 || hb.ContainerRunning != 1 {
					t.Errorf("心跳摘要错误: %+v", hb)
				}
				heartbeats.Add(1)
			}
		}
	}))
	defer srv.Close()

	agent, err := New(Options{
		HubURL:       "ws" + srv.URL[len("http"):],
		Token:        "tok",
		AgentVersion: "0.1.0",
		HeartbeatSec: 1,
		Logger:       quietLogger(),
		Runtime:      testRuntime{},
		Dispatch:     echoDispatcher,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	go func() { _ = agent.Run(ctx) }()

	// 等至少两次心跳（间隔 1 秒，留出余量）。
	deadline := time.Now().Add(5 * time.Second)
	for heartbeats.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if heartbeats.Load() < 2 {
		t.Fatalf("心跳次数 = %d，期望至少 2 次", heartbeats.Load())
	}
}

// TestReconnectAfterDisconnect 验证断线后会重连（退避不阻塞重连逻辑）。
func TestReconnectAfterDisconnect(t *testing.T) {
	var connects atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		n := connects.Add(1)
		var hello envelope.Envelope
		if err := c.ReadJSON(&hello); err != nil {
			c.Close()
			return
		}
		ack, _ := envelope.NewResponse(&hello, envelope.HelloAckPayload{AgentID: "a1", HeartbeatSec: 1})
		_ = c.WriteJSON(ack)

		if n == 1 {
			// 第一次连接：收到快照后立刻关闭，模拟 Hub 侧断开。
			var env envelope.Envelope
			_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
			_ = c.ReadJSON(&env)
			c.Close()
			return
		}
		// 第二次连接保持住，等测试结束。
		<-time.After(3 * time.Second)
		c.Close()
	}))
	defer srv.Close()

	agent, err := New(Options{
		HubURL:       "ws" + srv.URL[len("http"):],
		Token:        "tok",
		AgentVersion: "0.1.0",
		HeartbeatSec: 1,
		Logger:       quietLogger(),
		Runtime:      testRuntime{},
		Dispatch:     echoDispatcher,
		// 缩短首次退避，测试不必真等 1 秒；仍走同一个 nextBackoff 逻辑。
		SeedBackoff: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	go func() { _ = agent.Run(ctx) }()

	deadline := time.Now().Add(6 * time.Second)
	for connects.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if connects.Load() < 2 {
		t.Fatalf("连接次数 = %d，期望断线后重连至少 2 次", connects.Load())
	}
}

// TestHandshakeRejectedIsReported 验证 Hub 拒绝握手时不会 panic 且能读到原因。
func TestHandshakeRejectedIsReported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		var hello envelope.Envelope
		if err := c.ReadJSON(&hello); err != nil {
			return
		}
		fail := envelope.NewErrorResponse(&hello, envelope.NewError(envelope.CodeUnauthorized, "令牌无效"))
		_ = c.WriteJSON(fail)
	}))
	defer srv.Close()

	agent, err := New(Options{
		HubURL:       "ws" + srv.URL[len("http"):],
		Token:        "bad",
		AgentVersion: "0.1.0",
		Logger:       quietLogger(),
		Runtime:      testRuntime{},
		Dispatch:     echoDispatcher,
		SeedBackoff:  20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	// 握手被拒后应持续重试直到 ctx 取消；Run 返回 nil 而不是崩溃。
	if err := agent.Run(ctx); err != nil {
		t.Fatalf("Run 返回错误: %v", err)
	}
}

// TestNewMessageIDUnique 验证消息 ID 不重复（协议要求 ID 唯一以便配对）。
func TestNewMessageIDUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := newMessageID()
		if id == "" {
			t.Fatal("生成了空 ID")
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("ID 重复: %s", id)
		}
		seen[id] = struct{}{}
	}
}

// TestPersistAgentID 验证 agent_id 原子写入并可读回。
func TestPersistAgentID(t *testing.T) {
	dir := t.TempDir()
	if err := persistAgentID(dir, "agent-42"); err != nil {
		t.Fatalf("persistAgentID 失败: %v", err)
	}
	data, err := readFile(dir + "/agent_id")
	if err != nil {
		t.Fatal(err)
	}
	if jsonTrim(data) != "agent-42" {
		t.Fatalf("持久化内容 = %q", data)
	}
	if err := persistAgentID(dir, ""); err == nil {
		t.Fatal("空 agent_id 应被拒绝")
	}
}

// TestEnqueueBackpressure 验证出站队列满时返回错误而不是阻塞。
func TestEnqueueBackpressure(t *testing.T) {
	s := &session{out: make(chan *envelope.Envelope, 1)}
	env, _ := envelope.NewEvent("1", envelope.MethodHeartbeat, "agent:x", nil)
	if err := s.enqueue(env); err != nil {
		t.Fatalf("第一次入队不应失败: %v", err)
	}
	if err := s.enqueue(env); err == nil {
		t.Fatal("队列满时应返回错误（否则读循环会被写循环拖死）")
	}
}

// readFile 与 jsonTrim 是测试用的极小工具，避免为了读一个文件引入更多 import。
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func jsonTrim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}
