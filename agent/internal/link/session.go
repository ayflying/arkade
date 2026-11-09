package link

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"arkade/protocol/envelope"
)

// 连接器参数。
const (
	// writeWait 是单条消息的写超时。长连接上如果对端不读，写会永久阻塞，
	// 必须靠写超时把"半死连接"踢掉。
	writeWait = 10 * time.Second
	// pongWait 是等待 pong 的最大时长，只在 ping 保活时使用。
	pongWait = 60 * time.Second
	// pingPeriod 是 WebSocket 协议层 ping 的间隔，必须小于 pongWait。
	pingPeriod = 25 * time.Second
	// readLimit 是单条入站消息上限。控制信令不该有大包，超限直接断连，
	// 防止对端（或中间被篡改的端）用超大包打爆内存。
	readLimit = 8 << 20 // 8 MiB
	// maxMissedHeartbeats 是连续心跳失败多少次判定断线（01 架构 3.3：3 次）。
	maxMissedHeartbeats = 3
	// defaultHeartbeatSec 是 Hub 未指定心跳间隔时的兜底值。
	defaultHeartbeatSec = 15
	// writeQueueSize 是出站队列长度。太小会在突发响应时阻塞读循环，
	// 太大则会在慢连接上堆积内存，32 条对控制信令足够。
	writeQueueSize = 32
)

// session 是一次已建立的长连接。
//
// 并发模型（关键，改之前请先读懂）：
//   - 一个读循环 goroutine：只负责从连接读消息并分发；
//   - 一个写循环 goroutine：只负责从 out 队列取消息并写连接；
//   - 一个心跳 goroutine：按 Hub 指定间隔入队心跳事件。
//
// 这样保证任意时刻只有写循环一个 goroutine 调用 WriteMessage——
// gorilla/websocket 明确要求"一个连接同时只能有一个 writer"，
// 在多处直接写会随机产生 panic（concurrent write to websocket connection）。
type session struct {
	agent *Agent
	conn  *websocket.Conn
	out   chan *envelope.Envelope

	// heartbeatSec 由 HelloAck 下发；读循环写入，心跳 goroutine 读，
	// 因此用原子变量而不是普通字段。
	heartbeatSec int64
	// missed 记录连续未成功的心跳次数，只有心跳 goroutine 读写。
	missed int

	closeOnce atomic.Bool
}

// newSession 构造会话并启动写循环。
func newSession(a *Agent, conn *websocket.Conn) *session {
	s := &session{
		agent:        a,
		conn:         conn,
		out:          make(chan *envelope.Envelope, writeQueueSize),
		heartbeatSec: int64(a.opts.HeartbeatSec),
	}
	if s.heartbeatSec <= 0 {
		s.heartbeatSec = defaultHeartbeatSec
	}
	conn.SetReadLimit(readLimit)
	// WebSocket 协议层 pong 处理：只有收到 pong 才把读超时往后推，
	// 这样"对端进程还在但已经不处理消息"的情况也能被识别出来。
	_ = conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	return s
}

// enqueue 把信封放进出站队列。
//
// 队列满时不阻塞调用方（否则读循环会被写循环拖死，形成互等），
// 而是直接返回错误让上层判定连接不可用——控制信令宁可断线重连，
// 也不能在一条堵死的连接上无限堆积。
func (s *session) enqueue(env *envelope.Envelope) error {
	select {
	case s.out <- env:
		return nil
	default:
		return fmt.Errorf("出站队列已满（%d 条），连接已不可用", writeQueueSize)
	}
}

// writeLoop 是唯一的写者。
func (s *session) writeLoop() {
	ping := time.NewTicker(pingPeriod)
	defer ping.Stop()
	defer close(s.out)

	for {
		select {
		case env, ok := <-s.out:
			if !ok {
				return
			}
			_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := s.conn.WriteJSON(env); err != nil {
				s.agent.log.Warn("向 Hub 写消息失败，判定连接不可用", "error", err, "method", env.Method)
				s.conn.Close()
				return
			}
		case <-ping.C:
			_ = s.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := s.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				s.conn.Close()
				return
			}
		}
	}
}

// heartbeatLoop 按 Hub 指定间隔发送心跳。
//
// "心跳 3 次失败判定断线"的落地方式：心跳消息经出站队列发出；如果连续 3 次
// enqueue 失败（队列满/写循环已退出），或期间连接被关闭，就主动 Close 连接，
// 让读循环报错退出、外层进入退避重连。这里不等待 Hub 的 ack——
// 01 架构 3.3 的"未响应"在实践中等价于"消息发不出去或对端已死"，
// 靠 WebSocket ping/pong 与读超时兜底，比额外引入 ack 表更简单可靠。
func (s *session) heartbeatLoop(ctx context.Context) {
	for {
		sec := time.Duration(atomic.LoadInt64(&s.heartbeatSec)) * time.Second
		if sec <= 0 {
			sec = defaultHeartbeatSec * time.Second
		}
		timer := time.NewTimer(sec)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if !s.sendHeartbeat(ctx) {
			s.missed++
			s.agent.log.Warn("心跳发送失败", "连续失败", s.missed, "阈值", maxMissedHeartbeats)
		} else {
			s.missed = 0
		}
		if s.missed >= maxMissedHeartbeats {
			s.agent.log.Warn("心跳连续失败达到阈值，主动断开连接以触发重连")
			s.conn.Close()
			return
		}
	}
}

// sendHeartbeat 组装并投递一次心跳。
func (s *session) sendHeartbeat(ctx context.Context) bool {
	payload, err := s.agent.buildHeartbeat(ctx)
	if err != nil {
		// 心跳载荷里的容器计数只是摘要，取不到时退化为 0，不影响"我还活着"这个语义。
		s.agent.log.Debug("采集心跳摘要失败，使用空摘要", "error", err)
	}
	env, err := envelope.NewEvent(newMessageID(), envelope.MethodHeartbeat, s.agent.target(), payload)
	if err != nil {
		s.agent.log.Warn("构造心跳信封失败", "error", err)
		return false
	}
	if err := s.enqueue(env); err != nil {
		return false
	}
	s.agent.log.Debug("已发送心跳", "容器总数", payload.ContainerTotal, "运行中", payload.ContainerRunning)
	return true
}

// readLoop 读取并分发入站消息，连接异常时返回错误。
func (s *session) readLoop() error {
	for {
		var env envelope.Envelope
		if err := s.conn.ReadJSON(&env); err != nil {
			return err
		}
		s.handleInbound(&env)
	}
}

// methodSessionClose 是 Hub 在主动关闭连接前发来的通知方法名。
//
// 定义在 Agent 侧而不是 protocol 包里：这是一条单向通知事件，
// 不需要双向契约。Hub 与 Agent 各自定义同一个字符串常量即可，
// 不认识时退化为"直接断开"——不会更糟。
const methodSessionClose = "session.close"

// handleInbound 按类型分发入站信封。
func (s *session) handleInbound(env *envelope.Envelope) {
	switch env.Type {
	case envelope.TypeRequest:
		// Hub 下发的请求：交给与本地 CLI 共用的注册表处理，结果放入出站队列。
		// 处理放在独立 goroutine 里是必要的：某些方法（日志、停止容器）
		// 可能耗时数秒，同步处理会卡住读循环，连带心跳都发不出去。
		go s.serveRequest(env)
	case envelope.TypeAck:
		// Hub 对心跳/事件的确认，目前只用于日志与排障，不改变状态机。
		s.agent.log.Debug("收到 Hub ack", "method", env.Method, "id", env.ID)
	case envelope.TypeResponse, envelope.TypeEvent:
		// 服务端主动关闭通知：必须单独处理。
		//
		// 为什么这条重要：Hub 会在禁用服务器时先发这个事件再断开。
		// 如果按"非预期消息"丢弃，运维只能看到连接反复断开重连，
		// 永远不知道真正原因是"这台机器被管理员禁用了"。
		// 打印出来，至少日志里能查到明确答案。
		if env.Method == methodSessionClose {
			s.agent.log.Error("Hub 主动关闭了连接", "reason", closeReason(env))
			return
		}
		// 其余情况下 Agent 不向 Hub 发请求，收到 response 属于异常；
		// 记录后丢弃，不中断连接（Hub 可能在做兼容性探测）。
		s.agent.log.Debug("收到非预期消息类型", "type", env.Type, "method", env.Method)
	default:
		s.agent.log.Warn("收到未知类型信封，已忽略", "type", env.Type)
	}
}

// closeReason 从关闭通知里取出原因文案。
//
// 解析失败时给一句兜底说明而不是空串：日志里出现空的 reason
// 会让人以为是日志系统丢字段，实际是载荷格式问题，两者要能区分开。
func closeReason(env *envelope.Envelope) string {
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := envelope.DecodePayload(env, &payload); err != nil || payload.Reason == "" {
		return "Hub 未提供关闭原因"
	}
	return payload.Reason
}

// serveRequest 执行请求并把响应投递回出站队列。
func (s *session) serveRequest(req *envelope.Envelope) {
	resp := s.agent.dispatch(s.agent.ctx, req)
	if resp == nil {
		return
	}
	if err := s.enqueue(resp); err != nil {
		s.agent.log.Warn("回送响应失败", "method", req.Method, "error", err)
		s.conn.Close()
		return
	}
	if resp.Error != nil {
		s.agent.log.Warn("请求处理失败", "method", req.Method, "code", resp.Error.Code, "message", resp.Error.Message)
		return
	}
	s.agent.log.Debug("请求处理完成", "method", req.Method)
}

// close 关闭底层连接（幂等）。
func (s *session) close() {
	if s.conn == nil {
		return
	}
	// 幂等关闭：重连与心跳可能同时触发关闭，重复 Close 在 gorilla 里会返回错误，
	// 忽略即可，但用原子标记避免不必要的 panic 面。
	if s.closeOnce.Swap(true) {
		return
	}
	_ = s.conn.Close()
}

// newMessageID 生成消息 ID。
//
// 用 16 字节随机数的十六进制表示，而不是引入 uuid 依赖：协议只要求 ID 唯一，
// 不要求符合 RFC 4122 的格式。crypto/rand 保证跨进程不撞号（机器重启后也不会）。
func newMessageID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源不可用时退化为时间戳，至少保证同一时刻不重复；
		// 这种情况极罕见（熵池耗尽），不值得让整个连接失败。
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
