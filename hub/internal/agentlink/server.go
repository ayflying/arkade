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
	"fmt"
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

	// credentials 用来把专属密钥解析成 agent_id。
	//
	// 为 nil 时只支持共享 token（旧部署与本包的部分单元测试）。
	// 生产路径一定注入，见 runtime.go 的装配。
	credentials CredentialStore

	// guard 把"握手最终校验 + 写库 + 登记"与"撤销写库 + 断开"串行化。
	//
	// 没有它就有真实竞态：握手在 authorize 之后、Add 之前被 reset/delete，
	// 撤销方的 Close 找不到这条还没登记的连接，而握手会照常上线，
	// 甚至把已删除的记录重新写回数据库。
	// 详见 generation.go 的包注释（文件名沿用，内容已改为 authMu 方案）。
	guard *EnrollmentGuard
}

// SetEnrollmentGuard 注入握手临界区守卫。
func (s *Server) SetEnrollmentGuard(g *EnrollmentGuard) {
	s.guard = g
}

// Guard 返回本 Server 使用的守卫，供撤销路径（httpx）复用同一把锁。
//
// 返回 nil 表示没有注入——此时撤销路径应退化为"写库 + Close"，
// 与旧行为一致（本包的部分单元测试就属于这种情况）。
func (s *Server) Guard() *EnrollmentGuard {
	return s.guard
}

// CredentialStore 是按摘要查专属凭据的最小能力。
//
// 定义成窄接口而不是直接依赖 store 包：
//   - agentlink 不应知道凭据是怎么存的（表名、迁移）；
//   - 测试里给一个 map 就能跑，不需要建库。
type CredentialStore interface {
	// LookupAgentByTokenHash 返回摘要绑定的 agent_id。
	// 摘要不存在时返回 ErrTokenNotEnrolled。
	LookupAgentByTokenHash(hash string) (agentID string, err error)
	// HasAgentToken 判断某条记录是否已配置专属凭据。
	// 用于"共享 token 不得认领专属记录"这条规则。
	HasAgentToken(agentID string) (bool, error)
}

// ErrTokenNotEnrolled 表示该摘要没有对应的专属凭据。
var ErrTokenNotEnrolled = errors.New("该密钥不是专属接入密钥")

// SetCredentialStore 注入凭据查询实现。
func (s *Server) SetCredentialStore(cs CredentialStore) {
	s.credentials = cs
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
	auth, err := s.authorize(r)
	if err != nil {
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

	s.serve(ws, r, auth)
}

// authResult 是认证通过的结论。
type authResult struct {
	// boundAgentID 非空表示这是专属密钥，身份**以数据库为准**。
	// 这个值之后会覆盖掉 Header 与 hello 里上报的任何 ID。
	boundAgentID string
	// shared 表示用的是全局共享 token（旧路径）。
	shared bool
	// tokenHash 是本次认证所用密钥的摘要（专属路径才有）。
	//
	// 握手结束前会在临界区内用它做**最终复核**：
	// 确认这把密钥此刻依然有效。authorize 通过只是 preflight，
	// 之后到注册之间可能发生重置/删除。
	tokenHash string
}

// authorize 校验 Bearer Token 并解析出这张连接的身份来源。
//
// 两种 token 并存：
//
//  1. **专属密钥**：先按 SHA256 摘要查库。查到就以库里的 agent_id 为准，
//     完全忽略请求头 X-Arkade-Agent-Id 与 hello 里的任何 ID。
//     Agent 侧仍会把持久化的 ID 放在头里，那是给共享 token 路径用的；
//     在专属路径上它只是被无视，不参与任何判断——**不受信的数据不参与决策**。
//
//  2. **共享 token**：比对上配置里的全局 token 后，沿用旧的"按 Hostname
//     生成/复用自己的 ID"逻辑。但若那个 ID 属于预建记录或已配置专属凭据，
//     一律拒绝：否则任何拿到共享 token 的机器都能带一个 Header 声明
//     "我就是那台专属服务器"，专属密钥就形同虚设。
//
// 安全要求：只读请求头。查询串里的 token 一律忽略，因为 URL 会被
// 反向代理、访问日志、监控系统记录，属于凭据泄漏高危路径。
func (s *Server) authorize(r *http.Request) (authResult, error) {
	auth := r.Header.Get(envelope.HeaderAuthorization)
	if !strings.HasPrefix(auth, envelope.BearerPrefix) {
		return authResult{}, errors.New("缺少 Authorization: Bearer 头")
	}
	token := strings.TrimSpace(strings.TrimPrefix(auth, envelope.BearerPrefix))
	if token == "" {
		return authResult{}, errors.New("Authorization 头里的 token 为空")
	}

	// 先按专属密钥解析。顺序很重要：
	// 若先比共享 token，一把同时等于共享 token 的专属密钥会被当成共享身份，
	// 绑定关系就被绕过了（生成时已禁止同值，这里是第二道保险）。
	hash := HashEnrollmentToken(token)
	if id, err := s.resolveEnrolledByHash(hash); err == nil {
		// 注意这里**只做 preflight**，不读取任何"版本号"之类的快照。
		// 早先的设计在 authorize 里记下凭据版本，正是 bug 的来源：
		// "查摘要"与"读版本"是两步，中间可插入一次 reset，
		// 于是查到旧摘要（通过）却读到新版本（复核也通过）——旧密钥照样放行。
		// 现在所有判断都挪进临界区，在锁内重新查一次库。
		return authResult{boundAgentID: id, tokenHash: hash}, nil
	} else if !errors.Is(err, ErrTokenNotEnrolled) {
		// 数据库出错：**fail closed**。
		// 此时无法判断这把密钥是不是专属密钥，放行等于在数据库抖动期间
		// 临时关闭专属校验，而那是攻击者最想要的窗口。
		return authResult{}, fmt.Errorf("无法验证接入凭据: %w", err)
	}

	// 不是专属密钥，回落到共享 token。
	if s.agentToken == "" {
		return authResult{}, errors.New("Hub 未配置 agent_token，拒绝所有 Agent 建连")
	}
	if subtleCompare(token, s.agentToken) != 1 {
		// 统一文案：不区分"密钥不存在"与"共享 token 不匹配"，
		// 避免通过错误信息探测某把密钥是否存在。
		return authResult{}, errors.New("Token 不匹配")
	}
	return authResult{shared: true}, nil
}

// resolveEnrolledByHash 把密钥摘要解析成绑定身份。
//
// 返回 ErrTokenNotEnrolled 表示"这不是专属密钥"，可由调用方回落；
// 其他错误都是真实故障，必须向上抛。
func (s *Server) resolveEnrolledByHash(hash string) (string, error) {
	if s.credentials == nil {
		// 未注入凭据存储（旧部署或部分单元测试）→ 全部视为非专属。
		return "", ErrTokenNotEnrolled
	}
	id, err := s.credentials.LookupAgentByTokenHash(hash)
	if err != nil {
		// 摘要不存在 = 不是专属密钥，交给调用方回落共享路径。
		// 适配层（router 装配处）已把 store 的哨兵错误转成本包的
		// ErrTokenNotEnrolled，因此这里只认一个哨兵。
		if errors.Is(err, ErrTokenNotEnrolled) {
			return "", ErrTokenNotEnrolled
		}
		return "", err
	}
	if id == "" {
		return "", ErrTokenNotEnrolled
	}
	return id, nil
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

// runGuarded 在临界区内执行握手关键段；没有注入守卫时直接执行。
func (s *Server) runGuarded(ctx context.Context, auth authResult, agentID string, fn func() error) error {
	if s.guard == nil {
		// 未注入守卫（单元测试）：退化为直接执行。
		// 生产路径一定注入，见 runtime.go 的装配。
		return fn()
	}
	return s.guard.RunEnrollment(ctx, fn)
}

// revalidateEnrollment 在临界区内做最终校验。
//
// **必须与 OnAgentHello、Registry.Add 在同一临界区**，理由见 serve 里的说明。
// 它复核三件事：
//
//  1. 摘要当前仍绑定同一个 agent_id（重置会换摘要）；
//  2. 记录仍然存在（删除会让它消失）；
//  3. 记录未被禁用（禁用是最高优先级的拒绝）。
//
// 注意这里的"查库"必须在锁内做——若在锁外查、锁内只用查询结果，
// 撤销方就能在"查完"和"用结果"之间插入，竞态原样回来。
func (s *Server) revalidateEnrollment(ctx context.Context, auth authResult, agentID string) error {
	if auth.shared {
		// 共享 token 路径没有摘要可复核。
		// 它的撤销语义由 disabled 检查覆盖（见 OnAgentHello，
		// 那条检查同样发生在临界区内）。
		return nil
	}
	if s.credentials == nil {
		return nil
	}
	id, err := s.credentials.LookupAgentByTokenHash(auth.tokenHash)
	if err != nil {
		if errors.Is(err, ErrTokenNotEnrolled) {
			// 摘要查不到了 = 密钥在握手期间被重置或删除。
			return ErrCredentialRevoked
		}
		return fmt.Errorf("无法复核接入凭据: %w", err)
	}
	if id != auth.boundAgentID || id != agentID {
		// 身份对不上属于不该发生的情况，保守拒绝：
		// 任何"对不上"都不该放行。
		return ErrCredentialRevoked
	}
	return nil
}

// checkSharedIDAllowed 判断共享 token 能否使用某个 agent_id。
//
// **这是"专属"能否成立的关键一票。**
//
// 假如不检查：任何拿到共享 token 的机器，只要在 Header 里写
// X-Arkade-Agent-Id: <某台专属服务器的 id>，就能顶替那台服务器上线，
// 进而读取、操作它的容器——专属密钥带来的一切隔离都被绕过。
//
// 判定：
//   - 目标是预建记录（preprovisioned）→ 只接受专属密钥，拒绝共享；
//   - 目标已配置专属凭据 → 同上，拒绝。
//
// 查询失败 → 拒绝（fail closed）。放行等于在数据库抖动期间临时关掉这条规则。
func (s *Server) checkSharedIDAllowed(agentID string) error {
	if s.credentials == nil {
		// 未注入凭据存储：没有"专属"这个概念，保持旧行为。
		return nil
	}
	enrolled, err := s.credentials.HasAgentToken(agentID)
	if err != nil {
		// 注意：ErrAgentTokenNotFound 之类"不存在"不算失败，
		// 那是正常的新机器首次接入。真正的故障才拒绝。
		if errors.Is(err, ErrTokenNotEnrolled) {
			return nil
		}
		return fmt.Errorf("无法确认该服务器的接入方式，请稍后重试: %w", err)
	}
	if enrolled {
		return ErrSharedCannotClaim
	}
	return nil
}

// ErrSharedCannotClaim 表示共享 token 试图接入专属服务器。
var ErrSharedCannotClaim = errors.New(
	"该服务器已配置专属接入密钥，必须使用它的专属密钥连接；" +
		"共享 token 不能用于已被专属密钥绑定的服务器")

// serve 完成握手并驱动连接生命周期。
func (s *Server) serve(ws *websocket.Conn, r *http.Request, auth authResult) {
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

	// Agent 身份解析：
	//
	//   - 专属密钥：**以数据库绑定为准**，完全忽略 Header 与 hello 里的任何 ID。
	//     Agent 侧还会照常带上它持久化的 ID（那是共享模式的产物），
	//     这里直接丢弃。不受信数据不参与决策，是这条路径唯一稳妥的写法。
	//
	//   - 共享 token：沿用旧逻辑——优先复用 Agent 已持有的 ID（重连场景），
	//     否则按主机名生成稳定 ID。但会额外校验：目标 ID 若属于预建记录
	//     或已配置专属凭据，一律拒绝，防止用共享 token 冒名顶替专属服务器。
	agentID := auth.boundAgentID
	if agentID == "" {
		agentID = strings.TrimSpace(r.Header.Get(envelope.HeaderAgentID))
		if agentID == "" {
			agentID = buildAgentID(hello)
		}
		if err := s.checkSharedIDAllowed(agentID); err != nil {
			s.log.Warn("共享 token 试图接入专属服务器，拒绝连接",
				"agent_id", agentID, "host", hello.Hostname)
			refuseHandshake(ws, &helloEnv, agentID, err)
			return
		}
	}

	// 最终校验、落库、登记都在下面的临界区里完成，此处不做任何副作用。

	conn := newConnection(agentID, ws, s.log)
	conn.requestHandler = s.requestHandler
	_ = ws.SetReadDeadline(time.Time{}) // 握手完成，取消读超时，交给心跳判断存活

	// **把"最终校验 + 写库 + 登记"整体放进临界区。**
	//
	// 这是本改动的全部要点。逐个说明为什么三步必须在同一临界区：
	//
	//   - 只做"注册前再查一次库"不够：查与 Add 之间仍有窗口，
	//     那正是 check-then-act 竞态 —— 窗口能变小，但消不掉。
	//   - OnAgentHello 的**写库**也必须在内：删除竞态最严重的后果不是
	//     "上线了"，而是"被删的记录被重新写回数据库"。
	//     把写库放在锁外，撤销方删完记录后它还会被复活。
	//   - 撤销方（reset/delete）用同一把锁包住"写库 + Close"，
	//     于是两边完全串行：
	//         撤销先 → 这里校验失败，既不上线也不写库；
	//         这里先 → 撤销方的 Close 能查到并断开这条已登记的连接。
	//
	// 临界区内**不做网络 I/O**：下面写 HelloAck 放在锁外，
	// 否则一个慢客户端会把所有握手卡住。
	herr := s.runGuarded(ctx, auth, agentID, func() error {
		// 1) 最终凭据校验：确认这把密钥此刻仍然有效、未被禁用。
		if verr := s.revalidateEnrollment(ctx, auth, agentID); verr != nil {
			return verr
		}
		// 2) 落库。必须在锁内，且必须"只更新已存在的记录"
		//    （见 checkSharedIDAllowed / agentSink 的预建保护）。
		if herr := s.handler.OnAgentHello(ctx, agentID, hello); herr != nil {
			return herr
		}
		// 3) 登记在线。顺序不能颠倒：Agent 收到 ack 后会立刻发首个请求，
		//    若此时还没登记，Registry.Call 会返回 agent_offline，
		//    表现为「刚连上就报离线」的偶发故障。
		s.registry.Add(conn)
		return nil
	})
	if herr != nil {
		if errors.Is(herr, ErrGuardBusy) || errors.Is(herr, context.Canceled) {
			s.log.Warn("等待认证临界区失败，拒绝握手", "agent_id", agentID, "err", herr)
		} else {
			s.log.Warn("处理握手失败，拒绝连接", "err", herr, "agent_id", agentID, "host", hello.Hostname)
		}
		refuseHandshake(ws, &helloEnv, agentID, herr)
		return
	}

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
