package httpx

// agent_sink.go 实现 agentlink.HelloHandler：把 Agent 上报的事件落到数据库。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

// agentSink 是 Agent 事件到存储层的适配器。
//
// 设计原因：连接层不应该依赖具体存储实现，因此用接口隔开；
// 本结构体只做「收到什么就写什么」的翻译，不含业务判断。
type agentSink struct {
	db  *store.DB
	log *slog.Logger
}

// 编译期断言：agentSink 必须满足连接层声明的接口。
var _ agentlink.HelloHandler = (*agentSink)(nil)

// OnAgentHello 握手成功后写入/更新 Agent 记录。
//
// 用 Upsert 的原因：Agent 重连时同一条记录需要更新运行时信息与版本，
// 而不是插入新行——否则每次重启都会多出一台「幽灵服务器」。
//
// 被禁用的 Agent 在这里被拒：这是握手阶段，连接还没进入在线表，
// 拒绝只需要返回错误即可，不需要额外的断连逻辑。
func (s *agentSink) OnAgentHello(ctx context.Context, agentID string, info envelope.HelloPayload) error {
	// 禁用检查必须在写库之前。
	//
	// 如果先 Upsert 再检查，被禁用的 Agent 依然会刷新 last_seen 与 status=online，
	// 界面上一台"已禁用"的机器却显示刚活跃过——两个字段互相矛盾，
	// 运维看到这种状态无法判断禁用到底生效没有。
	//
	// Agent 不存在时 IsAgentDisabled 返回 ErrNotFound，那是正常的首次注册，
	// 不当作错误处理（首次连接的新机器当然还没有禁用标记）。
	disabled, err := s.db.IsAgentDisabled(ctx, agentID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		// 查询失败时拒绝连接而不是放行：宁可让一台好机器重连失败，
		// 也不让一台已被禁用的机器因为数据库抖动而挤进来。
		// 这类失败会在日志里留下明确记录，运维能据此判断是数据库问题。
		s.log.Error("握手时查询禁用状态失败，拒绝连接", "agent_id", agentID, "err", err)
		return fmt.Errorf("无法确认该服务器的授权状态，请稍后重试: %w", err)
	}
	if disabled {
		s.log.Warn("拒绝被禁用的 Agent 连接", "agent_id", agentID, "host", info.Hostname)
		// 返回的文案会被连接层写回给 Agent（见 serve 的 hello 拒绝路径），
		// 因此这里必须写成 Agent 运维能照做的中文说明，
		// 而不是给 Hub 内部看的英文或错误码。
		return errors.New(DisabledAgentMessage)
	}

	now := time.Now().UTC()

	// 名称：优先沿用已有记录的 Name，其次用本次上报的主机名，最后兜底文案。
	//
	// 为什么不能每次都直接用 info.Hostname 覆盖：
	// 用户可能在界面把显示名改成了「生产-东京」，但主机名永远是那串随机字符。
	// 每次重连都覆盖的话，用户改的名字会在下次重连时凭空消失。
	// 注意这只保护 Name 列；DisplayName 是用户独占的可编辑字段，
	// UpsertAgent 不碰它。
	name := info.Hostname
	if name == "" {
		name = "未命名主机(" + info.Arch + ")"
	}
	if existing, gerr := s.db.GetAgent(ctx, agentID); gerr == nil && existing.Name != "" {
		name = existing.Name
	}

	runtime := string(info.Runtime.Kind)
	if runtime == "" {
		// 运行时探测失败时明确写 none，让前端能正确降级展示，
		// 而不是显示一个空白字段让人误以为数据丢了。
		runtime = string(envelope.RuntimeNone)
	}

	rec := store.AgentRecord{
		ID:             agentID,
		Name:           name,
		Status:         "online",
		Version:        info.AgentVersion,
		Runtime:        runtime,
		RuntimeVersion: info.Runtime.Version,
		LastSeen:       now.Unix(),
		Labels:         map[string]string{},
		CreatedAt:      now.Unix(),
		UpdatedAt:      now.Unix(),
	}

	if err := s.db.UpsertAgent(ctx, rec); err != nil {
		return err
	}

	s.log.Info("Agent 握手完成",
		"agent_id", agentID,
		"host", info.Hostname,
		"version", info.AgentVersion,
		"runtime", runtime,
		"runtime_version", info.Runtime.Version,
	)
	return nil
}

// DisabledAgentMessage 是"该服务器已被禁用"的对外文案。
//
// 导出是因为连接层要用它把原因发给 Agent 对端：
// 只返回错误而不告知对端的话，Agent 只能看到"连接被关闭"，
// 会当成网络故障无限重连，运维也看不出是被人为禁用的。
const DisabledAgentMessage = "该服务器已被管理员禁用，请联系管理员在「服务器管理」中启用后再连接"

// OnAgentHeartbeat 更新最后活跃时间与在线状态。
// 心跳是最高频的消息，必须保持轻量：只做一次 UPDATE。
func (s *agentSink) OnAgentHeartbeat(ctx context.Context, info envelope.HeartbeatPayload) {
	at := info.At
	if at == 0 {
		at = time.Now().Unix()
	}
	if err := s.db.UpdateAgentStatus(ctx, info.AgentID, "online", at); err != nil {
		s.log.Warn("更新 Agent 心跳失败", "err", err, "agent_id", info.AgentID)
	}
}

// OnAgentSnapshot 处理重连后的全量快照。
//
// 当前阶段：容器列表的权威来源是实时查询 Agent，快照仅用于日志与后续对账，
// 因此这里只记录摘要，不落库。这样做避免把「快照缓存」做成第二套真相源，
// 导致界面显示与真实状态不一致。
func (s *agentSink) OnAgentSnapshot(ctx context.Context, agentID string, containers []envelope.ContainerSummary) {
	running := 0
	for _, c := range containers {
		if c.State == "running" {
			running++
		}
	}
	s.log.Info("收到 Agent 状态快照",
		"agent_id", agentID,
		"total", len(containers),
		"running", running,
	)
}

// OnAgentDisconnected 连接断开后把状态置为 offline。
func (s *agentSink) OnAgentDisconnected(ctx context.Context, agentID string) {
	if err := s.db.UpdateAgentStatus(ctx, agentID, "offline", time.Now().Unix()); err != nil {
		s.log.Warn("更新 Agent 离线状态失败", "err", err, "agent_id", agentID)
	}
}
