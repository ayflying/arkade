package agentlink

// server.go 是 Agent 的连接入口处理器。
//
// 职责：
//   1. 校验认证（只认请求头，忽略查询串里的 token——查询串会被代理与日志留存）；
//   2. 升级为 WebSocket，完成 hello 握手并下发 Hub 分配的 agent_id；
//   3. 把连接交给 Registry 管理，并驱动读循环直到断开；
//   4. 把心跳与快照事件交给上层持久化。

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"arkade/protocol/envelope"
)

// HelloHandler 处理握手事件后由上层落库的钩子。
// 拆成接口而不是直接依赖 store，便于测试与将来接入不同存储。
//
// 接口定义见 handler.go，此处不再重复声明。

// Server 是 Agent 连接服务器。
type Server struct {
	registry   *Registry
	agentToken string
	hubVersion string
	handler    HelloHandler
	log        *slog.Logger
	upgrader   websocket.Upgrader

	// requestHandler 用于应答 Agent 主动发起的请求（Type=request）。
	// 正常生产中 Hub 不向 Agent 发请求，这个钩子主要服务于联调与测试：
	// 它让测试可以模拟一个真实 Agent 对 container.list 等方法的应答。
	requestHandler func(agentID string, env *envelope.Envelope) *envelope.Envelope
}

// SetRequestHandler 注册 Agent 请求处理器（仅用于测试与联调）。
func (s *Server) SetRequestHandler(fn func(agentID string, env *envelope.Envelope) *envelope.Envelope) {
	s.requestHandler = fn
}

// NewServer 构造连接服务器。
//
// allowOriginCheck 由调用方传入：Hub 的 Agent 通道只接受 Agent 程序，
// 不涉及浏览器同源策略，因此这里关闭 Origin 校验，
// 身份完全依靠 Bearer Token——这一点必须在文档里写清楚，不能靠浏览器保护。
func NewServer(reg *Registry, agentToken, hubVersion string, handler HelloHandler, log *slog.Logger) *Server {
	return &Server{
		registry:   reg,
		agentToken: agentToken,
		hubVersion: hubVersion,
		handler:    handler,
		log:        log,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			// Agent 不是浏览器，Origin 头可能为空或任意值，
			// 认证由 Bearer Token 完成，故此处不校验 Origin。
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

// HandleConnect 处理 GET /agent/connect。
func (s *Server) HandleConnect(w http.ResponseWriter, r *http.Request) {
	if err := s.authorize(r); err != nil {
		s.log.Warn("Agent 建连认证失败", "remote", r.RemoteAddr, "err", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// 协议版本在升级前就能拒绝，避免建立无用连接。
	if v := r.Header.Get(envelope.HeaderProtoVersion); v != "" && v != "1" {
		http.Error(w, "unsupported protocol version", http.StatusUpgradeRequired)
		return
	}

	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade 内部已写过响应，这里只记录。
		s.log.Warn("WebSocket 升级失败", "remote", r.RemoteAddr, "err", err)
		return
	}

	s.serve(ws, r)
}

// authorize 校验 Bearer Token。
//
// 安全要求：只读请求头。查询串里的 token 一律忽略，因为 URL 会被
// 反向代理、访问日志、监控系统记录，属于凭据泄漏高危路径。
func (s *Server) authorize(r *http.Request) error {
	if s.agentToken == "" {
		return errors.New("Hub 未配置 agent_token，拒绝所有 Agent 建连")
	}
	auth := r.Header.Get(envelope.HeaderAuthorization)
	if !strings.HasPrefix(auth, envelope.BearerPrefix) {
		return errors.New("缺少 Authorization: Bearer 头")
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, envelope.BearerPrefix))
	if subtleCompare(token, s.agentToken) != 1 {
		return errors.New("Token 不匹配")
	}
	return nil
}

// refuseHandshake 把握手被拒的原因回给 Agent，然后关闭连接。
//
// 为什么要专门做这一步：握手阶段的拒绝如果用"直接断开"表达，
// Agent 侧看到的只是 WebSocket 被关闭，与网络故障、Hub 重启、
// 反向代理超时完全无法区分。Agent 的合理反应只能是不断重连，
// 而真正的答案（"你被禁用了"）永远在 Hub 日志里，运维不看服务器就无从得知。
//
// 回一条带 code/message 的响应信封，Agent 就能打印出准确原因
// 并在 UI 上提示"请联系管理员"，避免无意义的重连风暴。
func refuseHandshake(ws *websocket.Conn, req *envelope.Envelope, agentID string, cause error) {
	env := envelope.NewErrorResponse(req, &envelope.Error{
		Code:    envelope.CodeForbidden,
		Message: cause.Error(),
	})
	// 短暂延长写超时：此时读 deadline 还是握手时的 10 秒，
	// 若对端网络慢，写可能失败，但失败也无所谓——反正要断开了。
	_ = ws.SetWriteDeadline(time.Now().Add(3 * time.Second))
	if werr := ws.WriteJSON(env); werr != nil {
		_ = ws.Close()
		return
	}
	// 给对方一点时间把这条消息读走再关闭。
	// 不等的话，TCP 连接可能在数据刷出前就被 RST，对端收不到原因。
	_ = ws.SetWriteDeadline(time.Now().Add(time.Second))
	_ = ws.Close()
}

// serve 完成握手并驱动连接生命周期。
func (s *Server) serve(ws *websocket.Conn, r *http.Request) {
	// 握手超时：Agent 必须在限定时间内发出 hello，否则断开，
	// 防止半开连接占满文件描述符。
	_ = ws.SetReadDeadline(time.Now().Add(10 * time.Second))

	var helloEnv envelope.Envelope
	if err := ws.ReadJSON(&helloEnv); err != nil {
		s.log.Warn("读取 Handshake 失败", "err", err)
		_ = ws.Close()
		return
	}
	if helloEnv.Method != envelope.MethodHello || helloEnv.Type != envelope.TypeRequest {
		s.log.Warn("首个消息不是 agent.hello，拒绝连接", "method", helloEnv.Method, "type", helloEnv.Type)
		_ = ws.Close()
		return
	}

	var hello envelope.HelloPayload
	if err := envelope.DecodePayload(&helloEnv, &hello); err != nil {
		s.log.Warn("解析 hello 载荷失败", "err", err)
		_ = ws.Close()
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Agent 身份：优先复用 Agent 已持有的 ID（重连场景），否则按主机名生成稳定 ID。
	// 由 Hub 分配而非 Agent 自选，避免两台机器用同一个 ID 互相顶掉。
	agentID := strings.TrimSpace(r.Header.Get(envelope.HeaderAgentID))
	if agentID == "" {
		agentID = buildAgentID(hello)
	}

	// 先落库再进在线表：若写库失败则不接受该连接，
	// 避免出现「在线表里有、列表里没有」的幽灵在线状态。
	//
	// 握手被拒（含"该服务器已被禁用"）时，必须把原因回给 Agent 再关闭。
	// 直接关连接的话，Agent 侧只能看到"连接断了"，会当成网络故障
	// 无限重连，运维也无从知道是被人为禁用的。
	if err := s.handler.OnAgentHello(ctx, agentID, hello); err != nil {
		s.log.Error("处理握手失败", "err", err, "host", hello.Hostname)
		refuseHandshake(ws, &helloEnv, agentID, err)
		return
	}

	conn := newConnection(agentID, ws, s.log)
	conn.requestHandler = s.requestHandler
	_ = ws.SetReadDeadline(time.Time{}) // 握手完成，取消读超时，交给心跳判断存活

	// 顺序很关键：必须**先登记在线表再回 HelloAck**。
	//
	// 反过来写会产生真实竞态：Agent 收到 ack 后立刻下发首个请求，
	// 而此时连接尚未登记，Registry.Call 会返回 agent_offline，
	// 表现为「刚连上就报离线」的偶发故障。
	s.registry.Add(conn)

	// 回包 HelloAck，告知 Hub 分配的 ID 与心跳间隔要求。
	ack, err := envelope.NewResponse(&helloEnv, envelope.HelloAckPayload{
		AgentID:      agentID,
		HubVersion:   s.hubVersion,
		HeartbeatSec: int(heartbeatTimeout.Seconds() / 3),
	})
	if err != nil {
		s.log.Error("构造 HelloAck 失败", "err", err)
		s.registry.Remove(conn)
		_ = ws.Close()
		return
	}
	if err := ws.WriteJSON(ack); err != nil {
		s.log.Warn("发送 HelloAck 失败", "err", err, "agent_id", agentID)
		s.registry.Remove(conn)
		_ = ws.Close()
		return
	}

	s.log.Info("Agent 上线", "agent_id", agentID, "host", hello.Hostname,
		"runtime", hello.Runtime.Kind, "runtime_version", hello.Runtime.Version, "version", hello.AgentVersion)

	go conn.writeLoop()
	go conn.heartbeatWatch()

	// 读循环阻塞直到断开；断开后统一收尾。
	conn.readLoop(s.makeEventHandler())

	s.registry.Remove(conn)
	s.log.Info("Agent 离线", "agent_id", agentID)

	offCtx, offCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer offCancel()
	s.handler.OnAgentDisconnected(offCtx, agentID)
}

// makeEventHandler 构造事件分发函数，把心跳与快照转给上层处理。
func (s *Server) makeEventHandler() IncomingHandler {
	return func(agentID string, env *envelope.Envelope) {
		// 事件处理用独立的短超时上下文，不阻塞读循环太久。
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		switch env.Method {
		case envelope.MethodHeartbeat:
			var hb envelope.HeartbeatPayload
			if err := envelope.DecodePayload(env, &hb); err != nil {
				s.log.Warn("解析心跳失败", "err", err, "agent_id", agentID)
				return
			}
			// 以连接侧身份为准，不信任 payload 里的 agent_id（防止伪造他人的心跳）。
			hb.AgentID = agentID
			s.handler.OnAgentHeartbeat(ctx, hb)

		case envelope.MethodSnapshotPush:
			var snap envelope.ContainerListResult
			if err := envelope.DecodePayload(env, &snap); err != nil {
				s.log.Warn("解析快照失败", "err", err, "agent_id", agentID)
				return
			}
			s.handler.OnAgentSnapshot(ctx, agentID, snap.Containers)

		default:
			s.log.Debug("未知事件类型，已忽略", "method", env.Method, "agent_id", agentID)
		}
	}
}
