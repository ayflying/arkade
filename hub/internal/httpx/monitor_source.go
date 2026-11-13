package httpx

import (
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/monitor"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"encoding/json"
	"github.com/gogf/gf/v2/errors/gerror"
)

type monitorSource struct {
	db       *store.DB
	registry *agentlink.Registry
}

func (s *monitorSource) Agents(ctx context.Context) ([]monitor.Agent, error) {
	records, err := s.db.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]monitor.Agent, 0, len(records))
	for _, rec := range records {
		// 配置主动关闭的内置本机不是Agent失联，不产生离线告警。
		if rec.Disabled || rec.Status == store.StatusPending || (s.registry.IsReservedLocal(rec.ID) && !s.registry.IsLocal(rec.ID)) {
			continue
		}
		online := s.registry.IsOnline(rec.ID)
		out = append(out, monitor.Agent{ID: rec.ID, Name: rec.Name, Online: online, LastSeen: rec.LastSeen})
	}
	return out, nil
}
func (s *monitorSource) Snapshot(ctx context.Context, id string) (envelope.MonitorSnapshot, error) {
	raw, err := s.registry.Call(ctx, id, envelope.MethodContainerMonitorSnapshot, envelope.MonitorSnapshotPayload{})
	if err != nil {
		return envelope.MonitorSnapshot{}, gerror.New("监控采集失败：Agent离线、版本不支持或运行时不可用")
	}
	var snapshot envelope.MonitorSnapshot
	if json.Unmarshal(raw, &snapshot) != nil {
		return snapshot, gerror.New("监控快照格式无效")
	}
	return snapshot, nil
}
