package httpx

// agent_sink.go 实现 agentlink.HelloHandler：把 Agent 上报的事件落到数据库。

import (
	"context"
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
func (s *agentSink) OnAgentHello(ctx context.Context, agentID string, info envelope.HelloPayload) error {
	now := time.Now().UTC()

	// 名称：优先用已有记录的显示名，其次主机名，最后兜底文案。
	// 这样用户在界面改过的名字不会被心跳时的握手覆盖。
	name := info.Hostname
	if name == "" {
		name = "未命名主机(" + info.Arch + ")"
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
