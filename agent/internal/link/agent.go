package link

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// Dispatcher 是请求分发函数。
//
// 类型定义为函数而不是接口，是为了让 handler.Registry 直接赋值，
// 同时测试里可以塞一个极简假实现，不必构造完整注册表。
type Dispatcher func(ctx context.Context, req *envelope.Envelope) *envelope.Envelope

// Options 是连接器配置。
type Options struct {
	HubURL       string
	Token        string
	AgentID      string // 可能为空：首连时由 Hub 在 HelloAck 下发
	DataDir      string // 用于持久化 Hub 下发的 agent_id
	AgentVersion string
	HeartbeatSec int  // 兜底心跳间隔；Hub 在 HelloAck 里给出后以其为准
	InsecureTLS  bool // 跳过 TLS 校验（仅测试环境）
	Logger       *slog.Logger
	Runtime      runtime.Runtime
	Dispatch     Dispatcher
	// SeedBackoff 只用于测试：覆盖首次退避时长，避免单测真的等 1 秒。
	SeedBackoff time.Duration
	// Capabilities 返回能力清单（通常是 handler.Registry.Methods 的函数值）。
	// 用函数而不是切片，保证 hello 时才取真值，不依赖构造顺序。
	Capabilities func() []string
}

// Agent 是连接器主体，持有可跨重连复用的状态（agent_id、退避进度）。
type Agent struct {
	opts     Options
	log      *slog.Logger
	dialer   *dialer
	dispatch Dispatcher

	// ctx 是所有会话共享的父 context，Stop 时取消。
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	agentID string // 握手成功后由 Hub 下发，需要跨重连保留
	stable  bool   // 上一次会话是否成功握手（决定重连后退避是否清零）
}

// New 构造连接器。参数非法时立即报错（这些错误都是配置问题，不该等到重连时才暴露）。
func New(opts Options) (*Agent, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Runtime == nil {
		return nil, errors.New("link: Runtime 不能为空")
	}
	if opts.Dispatch == nil {
		return nil, errors.New("link: Dispatch 不能为空")
	}
	d, err := newDialer(opts.HubURL, opts.Token, opts.AgentID, opts.AgentVersion, envelope.Version, opts.InsecureTLS)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Agent{
		opts:     opts,
		log:      opts.Logger,
		dialer:   d,
		dispatch: opts.Dispatch,
		ctx:      ctx,
		cancel:   cancel,
		agentID:  opts.AgentID,
	}, nil
}

// Run 启动连接循环，直到 ctx 被取消。
//
// 循环结构：
//
//	dial → handshake → 收发 → 断线 → 退避 → 重新 dial
//
// 退避只在"连续失败"时累加；一旦完成握手（说明 Hub 与网络都正常），
// 下次断线的退避从 1s 重新开始——否则一次偶发断线会让 Agent 在接下来
// 几分钟里都连不上，运维会误判成 Hub 挂了。
func (a *Agent) Run(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		a.cancel()
	}()

	backoff := a.opts.SeedBackoff
	if backoff <= 0 {
		backoff = MinBackoff
	}

	for {
		if a.ctx.Err() != nil {
			return nil
		}

		err := a.runOnce()
		if a.ctx.Err() != nil {
			// 上层主动停止（SIGTERM / Ctrl+C），本次不会是运维关心的异常。
			return nil
		}

		if a.takeStable() {
			// 上次握手成功过，说明之前的连接是"活着然后断开"，而不是"一直连不上"。
			backoff = MinBackoff
		}
		if isCloseError(err) {
			a.log.Info("与 Hub 的连接被对端正常关闭，准备重连", "下次重连等待", backoff.String())
		} else {
			a.log.Warn("与 Hub 的连接已断开，准备重连", "error", err, "下次重连等待", backoff.String())
		}

		timer := time.NewTimer(backoff)
		select {
		case <-a.ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = nextBackoff(backoff)
	}
}

// runOnce 建立一次连接并跑到断开为止。
func (a *Agent) runOnce() error {
	conn, resp, err := a.dialer.dial()
	if err != nil {
		// 握手失败时给出可读原因：401/403 多半是 token 错，404 多半是路径错。
		detail := ""
		if resp != nil {
			detail = fmt.Sprintf("（HTTP %d）", resp.StatusCode)
			if resp.Body != nil {
				_ = resp.Body.Close()
			}
		}
		return fmt.Errorf("连接 Hub 失败%s: %w", detail, err)
	}

	sess := newSession(a, conn)
	defer sess.close()

	if err := a.handshake(sess); err != nil {
		return err
	}
	a.markStable()

	// 握手成功即视为"上次连接是有效的"，用于 Run 里复位退避。
	go sess.writeLoop()

	hbCtx, hbCancel := context.WithCancel(a.ctx)
	defer hbCancel()
	go sess.heartbeatLoop(hbCtx)

	// 重连（或首连）后必须上报全量快照：Hub 可能在断线期间错过了容器状态变化，
	// 只靠增量会留下"幽灵状态"。这一步放在心跳启动之后，保证快照失败不影响存活判定。
	if err := a.pushSnapshot(sess); err != nil {
		return fmt.Errorf("上报全量快照失败: %w", err)
	}

	a.log.Info("已连上 Hub", "agent_id", a.AgentID(), "hub", a.opts.HubURL)
	return sess.readLoop()
}

// handshake 发送 agent.hello 并等待 HelloAck。
//
// 为什么不用 request/response 信封而用 event + ack 语义：协议里 HelloAck 是
// Hub 回给 Agent 的 response（ReplyTo = hello 的 ID）。这里用简单的"等待下一条
// response 且回复 ID 匹配"来实现，超时 10 秒——握手阶段一旦卡住，退避重连会接手。
func (a *Agent) handshake(sess *session) error {
	hello, err := a.buildHello(context.Background())
	if err != nil {
		return fmt.Errorf("构造握手载荷失败: %w", err)
	}
	req, err := envelope.NewRequest(newMessageID(), envelope.MethodHello, a.target(), hello)
	if err != nil {
		return fmt.Errorf("构造握手信封失败: %w", err)
	}

	_ = sess.conn.SetWriteDeadline(time.Now().Add(writeWait))
	if err := sess.conn.WriteJSON(req); err != nil {
		return fmt.Errorf("发送握手失败: %w", err)
	}

	// 握手期间只允许收到这一条响应，采用同步读，避免与读循环争抢连接。
	_ = sess.conn.SetReadDeadline(time.Now().Add(handshakeWait))
	for {
		var env envelope.Envelope
		if err := sess.conn.ReadJSON(&env); err != nil {
			return fmt.Errorf("等待 Hub 握手应答失败: %w", err)
		}
		if env.Type != envelope.TypeResponse {
			// 握手期间收到非响应（例如 Hub 抢先推事件），丢弃并继续等。
			a.log.Debug("握手期间收到非响应消息，已忽略", "type", env.Type, "method", env.Method)
			continue
		}
		if env.ReplyTo != "" && env.ReplyTo != req.ID {
			continue
		}
		if env.Error != nil {
			// 认证失败、版本不兼容等都在这里返回，外层会按退避策略重试，
			// 但日志里必须说清楚，运维才知道该去改什么。
			return fmt.Errorf("Hub 拒绝握手: %s", env.Error.Error())
		}
		var ack envelope.HelloAckPayload
		if err := envelope.DecodePayload(&env, &ack); err != nil {
			return fmt.Errorf("解析握手应答失败: %w", err)
		}
		return a.applyHelloAck(sess, ack)
	}
}

// handshakeWait 是等待 HelloAck 的上限。
const handshakeWait = 10 * time.Second

// applyHelloAck 落地 Hub 下发的 agent_id 与心跳间隔。
func (a *Agent) applyHelloAck(sess *session, ack envelope.HelloAckPayload) error {
	if ack.AgentID != "" && ack.AgentID != a.AgentID() {
		a.setAgentID(ack.AgentID)
		// 同步更新握手头里的身份：重连时 Hub 需要靠 X-Arkade-Agent-Id 认出
		// 这是"同一个 Agent 回来了"，仍然带旧的空值会让 Hub 重建一条记录。
		a.dialer.setAgentID(ack.AgentID)
		// 持久化到数据目录：下次启动（包括 Hub 暂时不可用时）也能带着稳定身份上线，
		// 否则 Hub 会把同一台机器识别成新 Agent，用户的权限与标签配置全部丢失。
		if a.opts.DataDir != "" {
			if err := persistAgentID(a.opts.DataDir, ack.AgentID); err != nil {
				// 持久化失败不阻断连接：身份本次会话仍可用，只是重启后会变，
				// 记录为 warn 让运维去检查数据目录权限。
				a.log.Warn("保存 agent_id 失败，重启后身份可能变化", "error", err, "data_dir", a.opts.DataDir)
			} else {
				a.log.Info("已保存 Hub 下发的 agent_id", "agent_id", ack.AgentID, "data_dir", a.opts.DataDir)
			}
		}
	}

	sec := ack.HeartbeatSec
	if sec <= 0 {
		sec = a.opts.HeartbeatSec
	}
	if sec <= 0 {
		sec = defaultHeartbeatSec
	}
	sess.setHeartbeatSec(sec)

	a.log.Info("握手成功", "agent_id", a.AgentID(), "hub_version", ack.HubVersion, "心跳间隔秒", sec)
	return nil
}

// buildHello 组装 hello 载荷。
func (a *Agent) buildHello(ctx context.Context) (envelope.HelloPayload, error) {
	hostname, _ := hostname()
	info, err := a.opts.Runtime.Info(ctx)
	if err != nil {
		// 运行时信息取不到不应阻止上线：Hub 需要知道"这台机器连上了，但引擎异常"。
		a.log.Warn("获取运行时信息失败，按无运行时上报", "error", err)
		info = runtime.Info{Kind: runtime.KindNone}
	}
	return envelope.HelloPayload{
		AgentVersion: a.opts.AgentVersion,
		ProtoVersion: envelope.Version,
		Runtime: envelope.RuntimeInfo{
			Kind:     toProtoKind(info.Kind),
			Version:  info.Version,
			APIVer:   info.APIVer,
			Socket:   info.Endpoint,
			Rootless: info.Rootless,
		},
		Capabilities: a.capabilities(),
		Hostname:     hostname,
		OS:           goruntime.GOOS,
		Arch:         goruntime.GOARCH,
	}, nil
}

// capabilities 返回能力清单。
//
// 直接取注册表里已注册的方法名：这样"声明支持"与"实际能处理"永远一致，
// 不会出现 Hub 按能力清单下发、Agent 却回 unsupported 的错位。
// 注册表通过 Dispatch 无法反查方法名，所以由构造方显式传入（见 options.go 的辅助函数）。
func (a *Agent) capabilities() []string {
	if a.opts.Capabilities != nil {
		return a.opts.Capabilities()
	}
	return nil
}

// buildHeartbeat 采集心跳摘要。
func (a *Agent) buildHeartbeat(ctx context.Context) (envelope.HeartbeatPayload, error) {
	p := envelope.HeartbeatPayload{AgentID: a.AgentID(), At: time.Now().Unix()}

	// 心跳要快：给运行时查询一个较短的子超时，避免慢引擎拖累心跳节奏
	// 从而误触发"3 次失败判定断线"。
	hbCtx, cancel := context.WithTimeout(ctx, heartbeatQueryTimeout)
	defer cancel()
	containers, err := a.opts.Runtime.List(hbCtx, true)
	if err != nil {
		return p, err
	}
	p.ContainerTotal = len(containers)
	for _, c := range containers {
		if c.State == "running" {
			p.ContainerRunning++
		}
	}
	return p, nil
}

// heartbeatQueryTimeout 是心跳里采集摘要的超时。
const heartbeatQueryTimeout = 5 * time.Second

// pushSnapshot 上报全量快照。
//
// 快照内容就是一次完整的容器列表（与 container.list 的应答结构相同），
// 这样 Hub 侧只需要一套解析逻辑。上报时机在握手成功之后，因此必须用
// 已经更新的 agent_id 构造请求——否则 ContainerListResult 里的 agent_id
// 会停留在握手前的空值，Hub 无法把快照对到具体 Agent 上（联调时实际踩到过）。
func (a *Agent) pushSnapshot(sess *session) error {
	req, err := envelope.NewRequest(newMessageID(), envelope.MethodContainerList, a.target(), nil)
	if err != nil {
		return err
	}
	resp := a.dispatch(context.Background(), req)
	if resp == nil {
		return errors.New("本地分发未返回结果")
	}
	if resp.Error != nil {
		return fmt.Errorf("%s", resp.Error.Error())
	}

	// 用本地分发得到的结果，补齐/纠正 agent_id：分发期间可能还没有身份信息，
	// 这里以 Agent 当前的稳定身份为准，保证 Hub 侧能按 agent_id 对账。
	payload, err := a.stampSnapshotAgentID(resp.Payload)
	if err != nil {
		return err
	}
	env, err := envelope.NewEvent(newMessageID(), envelope.MethodSnapshotPush, a.target(), payload)
	if err != nil {
		return err
	}
	if err := sess.enqueue(env); err != nil {
		return err
	}
	a.log.Info("已上报全量容器快照", "agent_id", a.AgentID())
	return nil
}

// stampSnapshotAgentID 把快照结果里的 agent_id 补成本 Agent 的当前身份。
//
// 为什么不直接让 handler 填：handler 是纯本地处理器，它的上下文里没有
// "Hub 分配给我的身份"这件事（本地 CLI 调用时更是没有身份）。身份属于
// 连接层状态，因此在这里统一盖章，避免把连接耦合进处理器。
func (a *Agent) stampSnapshotAgentID(raw []byte) (json.RawMessage, error) {
	var res envelope.ContainerListResult
	if err := json.Unmarshal(raw, &res); err != nil {
		// 载荷不是预期的列表结果时不改写，原样上报（Hub 侧会报解析错误，
		// 但至少连接不会因为本地序列化问题中断）。
		return raw, nil
	}
	res.AgentID = a.AgentID()
	out, err := json.Marshal(res)
	if err != nil {
		return nil, fmt.Errorf("序列化快照失败: %w", err)
	}
	return out, nil
}

// AgentID 返回当前身份（可能为空：尚未握手）。
func (a *Agent) AgentID() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.agentID
}

// setAgentID 更新身份。
func (a *Agent) setAgentID(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.agentID = id
}

// target 生成本 Agent 的信封目标地址。
//
// 未分配 ID 时使用占位符：Hub 在握手中尚未知道是谁，但仍需要一个非空 target。
func (a *Agent) target() string {
	id := a.AgentID()
	if id == "" {
		return "agent:pending"
	}
	return "agent:" + id
}

// markStable 标记"上一次会话握手成功过"。
func (a *Agent) markStable() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stable = true
}

// takeStable 取出并清除 stable 标记（一次性语义，避免重复复位退避）。
func (a *Agent) takeStable() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	ok := a.stable
	a.stable = false
	return ok
}

// setHeartbeatSec 由 HelloAck 更新会话心跳间隔。
func (s *session) setHeartbeatSec(sec int) {
	if sec <= 0 {
		return
	}
	setAtomicInt64(&s.heartbeatSec, int64(sec))
}

// toProtoKind 把运行时 Kind 映射为协议类型。
func toProtoKind(k runtime.Kind) envelope.RuntimeKind {
	switch k {
	case runtime.KindDocker:
		return envelope.RuntimeDocker
	case runtime.KindPodman:
		return envelope.RuntimePodman
	default:
		return envelope.RuntimeNone
	}
}

// isCloseError 判断读循环返回的错误是否是"正常关闭"。
//
// 通过命令行 Ctrl+C 或 systemd stop 触发的关闭会得到 1000/1001，此时
// 不值得打 error 级日志，否则运维会被大量"连接已关闭"噪声淹没。
// 目前由 runOnce 的调用方在日志里用于降级展示（见 Run 的日志分支）。
func isCloseError(err error) bool {
	if err == nil {
		return false
	}
	return websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseNoStatusReceived)
}
