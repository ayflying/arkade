package link

// session_cancel_test.go：验证 Agent 能按 Hub 的取消通知真正停手。
//
// 为什么这条必须有测试：取消失效不会报错，只会让一次"用户已经放弃的读取"
// 继续占着并发槽位（helper 同时最多 2 个），表现成之后所有文件操作都提示
// 「文件操作繁忙」。这种故障在日志里几乎看不出来，只能靠契约测试守住。

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

// newCancelTestSession 造一个只够跑 serveRequest 的会话。
func newCancelTestSession(t *testing.T, dispatch Dispatcher) *session {
	t.Helper()
	return &session{
		agent: &Agent{
			log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			dispatch: dispatch,
			// serveRequest 会以父 ctx 派生每个请求的 ctx，nil 父会 panic
			ctx: context.Background(),
		},
		out: make(chan *envelope.Envelope, 1),
	}
}

// TestServeRequestCancelledByHub 是本次修复的核心回归：
// Hub 发来 request.cancel 后，处理函数收到的 ctx 必须结束。
func TestServeRequestCancelledByHub(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	finished := make(chan struct{})

	s := newCancelTestSession(t, func(ctx context.Context, _ *envelope.Envelope) *envelope.Envelope {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil
	})

	req, err := envelope.NewRequest("req-cancel", envelope.MethodVolumeFiles, "agent:x", map[string]string{})
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	// 与读循环一致：先登记再执行，这样取消不会因为 goroutine 未启动而未命中
	ctx, cancel := context.WithCancel(s.agent.ctx)
	defer cancel()
	s.trackRequest(req.ID, cancel)
	go func() {
		s.serveRequest(ctx, req)
		close(finished)
	}()

	<-started
	// 模拟 Hub 下发的取消事件（真实链路上它由读循环调用 handleCancel）
	ev, err := envelope.NewEvent("cancel-1", envelope.EventRequestCancel, "agent:x",
		envelope.RequestCancelPayload{ID: req.ID})
	if err != nil {
		t.Fatalf("构造取消事件失败: %v", err)
	}
	s.handleCancel(ev)

	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("取消后处理函数没有收到 ctx 结束（Agent 会继续占着并发槽位）")
	}
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("取消后请求没有收尾")
	}

	// 请求结束后必须注销，否则取消表会随会话无限增长
	s.cancelsMu.Lock()
	remaining := len(s.cancels)
	s.cancelsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("请求结束后仍残留 %d 条取消登记", remaining)
	}
}

// TestCancelRequestUnknownIDIsNoop 守住"取消迟到/重复"的常见情形：
// 请求已经做完并注销，此时再收到取消不能报错，也不能误伤别的请求。
func TestCancelRequestUnknownIDIsNoop(t *testing.T) {
	s := newCancelTestSession(t, func(context.Context, *envelope.Envelope) *envelope.Envelope { return nil })
	if s.cancelRequest("nobody") {
		t.Fatal("未知 ID 不应命中")
	}
	if s.cancelRequest("") {
		t.Fatal("空 ID 不应命中")
	}
}

// TestHandleCancelIgnoresBadPayload 保证一条坏消息不会打穿读循环：
// 解析失败只记日志，连接继续可用（否则一次取消就能让整条链路重连）。
func TestHandleCancelIgnoresBadPayload(t *testing.T) {
	s := newCancelTestSession(t, func(context.Context, *envelope.Envelope) *envelope.Envelope { return nil })
	s.handleCancel(&envelope.Envelope{
		V:       envelope.Version,
		ID:      "bad",
		Type:    envelope.TypeEvent,
		Method:  envelope.EventRequestCancel,
		Payload: []byte(`"not-an-object"`),
	})
	// 不应 panic，且不应登记任何东西
	s.cancelsMu.Lock()
	remaining := len(s.cancels)
	s.cancelsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("坏载荷不应登记取消，实际 %d 条", remaining)
	}
}

// TestCancelRequestIsSingleShot 验证一次取消只生效一次：
// 重复取消同一个 ID 不应再次调用 cancel（避免误伤已复用的槽位）。
func TestCancelRequestIsSingleShot(t *testing.T) {
	s := newCancelTestSession(t, func(context.Context, *envelope.Envelope) *envelope.Envelope { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.trackRequest("req-1", cancel)

	if !s.cancelRequest("req-1") {
		t.Fatal("第一次取消应命中")
	}
	if s.cancelRequest("req-1") {
		t.Fatal("第二次取消不应命中")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("ctx 未被取消")
	}
}
