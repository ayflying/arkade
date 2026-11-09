package agentlink

// connection.go 是单个 Agent 连接的状态机。
//
// 一个连接包含三条并发路径：
//   - 读循环：从 WebSocket 读信封，分发到事件处理或唤醒等待的请求；
//   - 写通道：所有写操作经同一个 goroutine 串行执行（WebSocket 不允许并发写）；
//   - 心跳监测：超过阈值未收到任何消息即断开，防止"幽灵在线"。

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"arkade/protocol/envelope"
)

// connection 表示一个已建连的 Agent。
type connection struct {
	agentID        string
	ws             *websocket.Conn
	log            *slog.Logger
	enqueue        chan []byte         // 出站队列，由写 goroutine 消费
	pending        map[string]*pending // 在途请求：请求 ID → 等待槽
	mu             sync.Mutex          // 保护 pending 与 closed
	closed         bool
	closeCh        chan struct{} // 关闭信号，供读/写/心跳三条路径感知
	lastSeen       time.Time     // 最后收到消息的时间，受 mu 保护
	requestHandler func(agentID string, env *envelope.Envelope) *envelope.Envelope
}

// newConnection 创建一个连接对象，并启动写与心跳两个 goroutine。
func newConnection(agentID string, ws *websocket.Conn, log *slog.Logger) *connection {
	return &connection{
		agentID:  agentID,
		ws:       ws,
		log:      log.With("agent_id", agentID),
		enqueue:  make(chan []byte, writeQueueSize),
		pending:  make(map[string]*pending),
		closeCh:  make(chan struct{}),
		lastSeen: time.Now(),
	}
}

// touch 更新最后活跃时间。
func (c *connection) touch() {
	c.mu.Lock()
	c.lastSeen = time.Now()
	c.mu.Unlock()
}

// idleDuration 返回距上次活跃的时长。
func (c *connection) idleDuration() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Since(c.lastSeen)
}

// send 把信封放入出站队列。
//
// 队列满时返回错误而不是阻塞：阻塞会把慢消费的 Agent 风险传导给所有调用方，
// 而正确做法是让这一个连接断开重连。
func (c *connection) send(env *envelope.Envelope) error {
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	select {
	case c.enqueue <- raw:
		return nil
	case <-c.closeCh:
		return ErrClosed
	default:
		return errors.New("出站队列已满，连接可能已阻塞")
	}
}

// writeLoop 是唯一的写 goroutine。WebSocket 不允许并发写，所有写入必须串行经过这里。
func (c *connection) writeLoop() {
	defer c.close()

	for {
		select {
		case raw := <-c.enqueue:
			if err := c.ws.WriteMessage(websocket.TextMessage, raw); err != nil {
				c.log.Warn("写消息失败，断开连接", "err", err)
				return
			}
		case <-c.closeCh:
			return
		}
	}
}

// heartbeatWatch 负责检测长时间无消息的连接。
func (c *connection) heartbeatWatch() {
	ticker := time.NewTicker(heartbeatTimeout / 4)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if c.idleDuration() > heartbeatTimeout {
				c.log.Warn("心跳超时，判定离线", "idle", c.idleDuration().String())
				c.close()
				return
			}
		case <-c.closeCh:
			return
		}
	}
}

// readLoop 持续读取并分发信封，直到连接断开。
func (c *connection) readLoop(handler IncomingHandler) {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			if !errors.Is(err, websocket.ErrCloseSent) {
				c.log.Info("连接读取结束", "err", err)
			}
			c.close()
			return
		}
		c.touch()

		var env envelope.Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			c.log.Warn("收到无法解析的信封，已丢弃", "err", err)
			continue
		}
		if verr := env.Validate(); verr != nil {
			c.log.Warn("收到非法信封，已丢弃", "code", verr.Code, "msg", verr.Message)
			continue
		}

		switch env.Type {
		case envelope.TypeResponse:
			c.deliver(&env)
		case envelope.TypeEvent:
			// Hub 侧收到的 event 一律交给上层处理（心跳、快照）。
			handler(c.agentID, &env)
		case envelope.TypeRequest:
			// Agent 主动发请求不是常态，仅在联调/测试注入处理器时才应答。
			// 未注入时记录并忽略，绝不让一个未定义行为阻塞读循环。
			if c.requestHandler != nil {
				resp := c.requestHandler(c.agentID, &env)
				if resp != nil {
					if err := c.send(resp); err != nil {
						c.log.Warn("应答 Agent 请求失败", "err", err, "method", env.Method)
					}
				}
				continue
			}
			c.log.Warn("Agent 主动发起了请求，协议未定义，已忽略", "method", env.Method)
		default:
			// ack 当前无使用场景，收到时更新活跃时间即可（已由 touch 处理）。
		}
	}
}

// deliver 把响应交给等待中的请求。
func (c *connection) deliver(env *envelope.Envelope) {
	c.mu.Lock()
	p, ok := c.pending[env.ReplyTo]
	if ok {
		delete(c.pending, env.ReplyTo)
	}
	c.mu.Unlock()

	if !ok {
		// 迟到的或重复的响应，直接丢弃，不影响其它请求。
		c.log.Debug("收到无对应请求的响应，已丢弃", "reply_to", env.ReplyTo)
		return
	}

	res := result{payload: env.Payload}
	if env.Error != nil {
		res.err = &ConnError{Code: env.Error.Code, Message: env.Error.Message}
	}
	// 用非阻塞发送兜底：若等待方已因超时离开，这里不能永久阻塞读循环。
	select {
	case p.ch <- res:
	default:
	}
}

// registerPending 登记一个在途请求，返回等待通道与清理函数。
func (c *connection) registerPending(id string) chan result {
	ch := make(chan result, 1)
	c.mu.Lock()
	c.pending[id] = &pending{ch: ch}
	c.mu.Unlock()
	return ch
}

// removePending 移除在途请求（超时清理）。
func (c *connection) removePending(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// close 关闭连接，唤醒所有等待中的请求。
//
// idempotent：多次调用安全，因为读循环、写循环、心跳监测都可能触发关闭。
func (c *connection) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pendingIDs := make([]string, 0, len(c.pending))
	for id := range c.pending {
		pendingIDs = append(pendingIDs, id)
	}
	c.mu.Unlock()

	close(c.closeCh)
	_ = c.ws.Close()

	// 唤醒所有等待者，避免调用方一直挂在 context 超时上。
	for _, id := range pendingIDs {
		c.removePending(id)
	}
}

// IncomingHandler 处理 Agent 主动上报的事件（心跳、快照）。
type IncomingHandler func(agentID string, env *envelope.Envelope)

// requestContext 是 request 的上下文辅助，保证超时与断连都能及时返回。
func (c *connection) waitResponse(ctx context.Context, id string, ch chan result, method string) (json.RawMessage, *ConnError) {
	select {
	case res := <-ch:
		if res.err != nil {
			return nil, res.err
		}
		return res.payload, nil
	case <-ctx.Done():
		c.removePending(id)
		return nil, newTimeoutError(c.agentID, method)
	case <-c.closeCh:
		c.removePending(id)
		return nil, newOfflineError(c.agentID)
	}
}
