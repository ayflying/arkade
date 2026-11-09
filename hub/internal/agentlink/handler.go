package agentlink

// handler.go 定义连接层向业务层回调的接口。
//
// 为什么把 agent_id 一起传：握手回调发生在 Hub 分配 ID 之后，
// 只有把 ID 一并交给业务层，业务层才能写入权威记录；
// 否则会出现「按主机名写一条、按 ID 再写一条」的重复记录问题。

import (
	"context"

	"arkade/protocol/envelope"
)

// HelloHandler 是连接层对业务层的事件回调集合。
type HelloHandler interface {
	// OnAgentHello 握手成功后调用。agentID 是 Hub 分配或复用的身份。
	OnAgentHello(ctx context.Context, agentID string, info envelope.HelloPayload) error
	// OnAgentHeartbeat 处理心跳，更新最后活跃时间与在线状态。
	OnAgentHeartbeat(ctx context.Context, info envelope.HeartbeatPayload)
	// OnAgentSnapshot 处理重连后的全量快照，用于 Hub 对账。
	OnAgentSnapshot(ctx context.Context, agentID string, containers []envelope.ContainerSummary)
	// OnAgentDisconnected 在连接断开后更新离线状态。
	OnAgentDisconnected(ctx context.Context, agentID string)
}
