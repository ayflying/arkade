package httpx

// gated_hello_test.go 提供可挂起握手的 HelloHandler（竞态测试专用）。
//
// 用途：把握手停在**已通过 authorize、尚未落库登记**这个精确位置，
// 让测试能在那一瞬间插入真实的 HTTP 撤销请求（reset/delete/disable）。
//
// 为什么不用 sleep 撞运气：那种测试要么偶发失败、要么偶发通过，
// 而"偶发通过"对一个安全属性测试来说等于没有测试。

import (
	"context"
	"sync"

	"arkade/protocol/envelope"
)

// gatedHello 把真实的 agentSink 包一层，可在指定时刻阻塞。
//
// 未 arm 时完全透传，不影响其它测试。
// 这也是它必须包在同一个 Server 上的原因：
// 若另建一个 Server，撤销接口作用的又是另一套 registry/guard，
// 测出来的就不是生产装配。
type gatedHello struct {
	sink *agentSink

	mu sync.Mutex
	// entered 在第一次真正进入 OnAgentHello 时关闭（仅 arm 后有效）。
	entered chan struct{}
	// release 关闭后放行。
	release chan struct{}
	once    sync.Once
}

// 以下三个方法名与字段只需要能构造：agentSink 是包内类型。
func (h *gatedHello) OnAgentHello(ctx context.Context, agentID string, info envelope.HelloPayload) error {
	h.mu.Lock()
	release, entered := h.release, h.entered
	h.mu.Unlock()

	if release != nil {
		// 通知测试"已进入"，然后等待放行。
		h.once.Do(func() {
			if entered != nil {
				close(entered)
			}
		})
		select {
		case <-release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return h.sink.OnAgentHello(ctx, agentID, info)
}

func (h *gatedHello) OnAgentHeartbeat(ctx context.Context, info envelope.HeartbeatPayload) {
	h.sink.OnAgentHeartbeat(ctx, info)
}

func (h *gatedHello) OnAgentSnapshot(ctx context.Context, agentID string, containers []envelope.ContainerSummary) {
	h.sink.OnAgentSnapshot(ctx, agentID, containers)
}

func (h *gatedHello) OnAgentDisconnected(ctx context.Context, agentID string) {
	h.sink.OnAgentDisconnected(ctx, agentID)
}

// arm 让下一次握手停住，返回"已进入"与"放行"两个 channel。
func (h *gatedHello) arm() (entered <-chan struct{}, release chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entered = make(chan struct{})
	h.release = make(chan struct{})
	h.once = sync.Once{}
	return h.entered, h.release
}
