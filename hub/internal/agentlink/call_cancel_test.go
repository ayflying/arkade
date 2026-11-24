package agentlink

// call_cancel_test.go：验证"Hub 放弃等待时通知 Agent 停手"这条契约。
//
// 背景（真实故障）：卷文件操作在 Agent 侧有并发闸门（同时最多 2 个 helper 容器）。
// 用户打开大文件后切走页面，浏览器断开，Hub 的请求 ctx 结束——如果此时
// 只删掉 pending 而不通知 Agent，Agent 会继续跑满自己那 60 秒，
// 期间同一台服务器上任何文件操作都返回「文件操作繁忙（同时最多 2 个）」。
//
// 这里用真实 WebSocket 对连接测，而不是打桩 Registry：需要确认的正是
// "取消消息真的从连接上发出去了"，打桩会把要验的东西替掉。

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"arkade/protocol/envelope"
)

// newCancelTestPair 建立一对已连接的 WebSocket，返回 Hub 侧注册表与 Agent 侧原始连接。
func newCancelTestPair(t *testing.T, agentID string) (*Registry, *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, req, nil)
		if err == nil {
			accepted <- ws
		}
	}))
	t.Cleanup(server.Close)

	agentConn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("建立测试连接失败: %v", err)
	}
	t.Cleanup(func() { _ = agentConn.Close() })

	c := newConnection(agentID, <-accepted, slog.Default())
	// 与 server.go 的真实握手路径保持一致：
	//   - 写循环必须启动，否则 send 只把消息塞进队列、永远不落到连接上；
	//   - 读循环必须启动，否则 Agent 的响应不会被 deliver 回 pending。
	go c.writeLoop()
	go c.readLoop(func(string, *envelope.Envelope) {})
	t.Cleanup(c.close)
	r := NewRegistry()
	r.Add(c)
	return r, agentConn
}

// readEnvelope 从 Agent 侧读取一条 Hub 下发的信封。
func readEnvelope(t *testing.T, ws *websocket.Conn) *envelope.Envelope {
	t.Helper()
	_ = ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	var env envelope.Envelope
	if err := ws.ReadJSON(&env); err != nil {
		t.Fatalf("读取 Hub 下发的信封失败: %v", err)
	}
	return &env
}

// TestCallSendsCancelWhenCallerGivesUp 是本次修复的核心回归。
//
// 断言三件事，缺一都不能真正解决"槽位被放弃的请求占住"：
//  1. 放弃等待后 Call 返回超时错误（调用方语义不变）；
//  2. Agent 侧确实收到 request.cancel 事件；
//  3. 取消指向的正是这次请求的 ID（发错 ID 等于取消别人）。
func TestCallSendsCancelWhenCallerGivesUp(t *testing.T) {
	r, agentConn := newCancelTestPair(t, "agent-cancel")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *ConnError, 1)
	go func() {
		_, err := r.Call(ctx, "agent-cancel", envelope.MethodVolumeFiles, map[string]string{"volume": "v"})
		done <- err
	}()

	req := readEnvelope(t, agentConn)
	if req.Type != envelope.TypeRequest || req.Method != envelope.MethodVolumeFiles {
		t.Fatalf("第一条消息应为请求，实际 type=%s method=%s", req.Type, req.Method)
	}
	if req.ID == "" {
		t.Fatal("请求 ID 为空，无法配对取消")
	}

	// 模拟浏览器断开：Hub 侧 ctx 结束
	cancel()
	if err := <-done; err == nil || err.Code != envelope.CodeTimeout {
		t.Fatalf("放弃等待应返回 timeout，实际 %v", err)
	}

	ev := readEnvelope(t, agentConn)
	if ev.Type != envelope.TypeEvent || ev.Method != envelope.EventRequestCancel {
		t.Fatalf("应收到取消事件，实际 type=%s method=%s", ev.Type, ev.Method)
	}
	var p envelope.RequestCancelPayload
	if err := envelope.DecodePayload(ev, &p); err != nil {
		t.Fatalf("取消载荷无法解析: %v", err)
	}
	if p.ID != req.ID {
		t.Fatalf("取消 ID = %q，期望 %q", p.ID, req.ID)
	}
}

// TestCallDoesNotCancelAfterNormalResponse 守住相反方向：
// 正常拿到响应后不应再发取消——否则会把"刚完成"误报成"已中止"，
// 也给连接增加无意义的消息。
func TestCallDoesNotCancelAfterNormalResponse(t *testing.T) {
	r, agentConn := newCancelTestPair(t, "agent-ok")

	done := make(chan *ConnError, 1)
	go func() {
		_, err := r.Call(context.Background(), "agent-ok", envelope.MethodVolumeFiles, map[string]string{"volume": "v"})
		done <- err
	}()

	req := readEnvelope(t, agentConn)
	resp, err := envelope.NewResponse(req, map[string]string{"path": ""})
	if err != nil {
		t.Fatalf("构造响应失败: %v", err)
	}
	if err := agentConn.WriteJSON(resp); err != nil {
		t.Fatalf("回写响应失败: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("正常响应不应报错: %v", err)
	}

	// 之后不应再有任何消息（短超时读取应超时）
	_ = agentConn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var extra envelope.Envelope
	if err := agentConn.ReadJSON(&extra); err == nil {
		t.Fatalf("正常完成后不应再收到消息，实际 method=%s type=%s", extra.Method, extra.Type)
	}
}
