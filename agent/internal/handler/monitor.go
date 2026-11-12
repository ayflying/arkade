package handler

import (
	"context"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// newMonitorSnapshotHandler 通过可选能力接线；既有 Runtime fake 无需添加方法。
func newMonitorSnapshotHandler(rt runtime.Runtime) Handler {
	var collector *runtime.MonitorCollector
	if cap, ok := rt.(runtime.ContainerMonitorRuntime); ok {
		collector = cap.NewMonitorCollector()
	}
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.MonitorSnapshotPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if collector == nil {
			return nil, envelope.NewError(envelope.CodeUnsupported, "当前引擎不支持容器监控")
		}
		s := collector.Snapshot(ctx)
		out := envelope.MonitorSnapshot{At: s.At, Complete: s.Complete, Error: s.Error, Containers: make([]envelope.ContainerMetric, 0, len(s.Containers))}
		for _, c := range s.Containers {
			out.Containers = append(out.Containers, envelope.ContainerMetric{
				ID: c.ID, Name: c.Name, Image: c.Image, State: c.State, Health: c.Health,
				ExitCode: c.ExitCode, RestartCount: c.RestartCount, StartedAt: c.StartedAt, FinishedAt: c.FinishedAt,
				CPUPercent: c.CPUPercent, MemoryBytes: c.MemoryBytes, MemoryLimitBytes: c.MemoryLimitBytes, MemoryPercent: c.MemoryPercent,
				NetworkRXBytes: c.NetworkRXBytes, NetworkTXBytes: c.NetworkTXBytes, BlockReadBytes: c.BlockReadBytes, BlockWriteBytes: c.BlockWriteBytes,
				StatsAvailable: c.StatsAvailable, Error: c.Error,
			})
		}
		return out, nil
	}
}
