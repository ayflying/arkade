package handler

// container_detail.go：container.inspect 处理器。
//
// 只读。把 runtime 的窄视图转换成协议结构。
// **这里不接触 daemon 原始 JSON**：裁剪在 runtime 层已经完成，
// 本文件只做字段搬运，因此不存在"某个版本不小心带出 Env"的风险。

import (
	"context"
	"strings"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// newInspectHandler 构造容器详情处理器。
func newInspectHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		var p envelope.ContainerInspectPayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		if strings.TrimSpace(p.ContainerID) == "" {
			return nil, badRequestf("容器 ID 不能为空")
		}

		cap, ok := rt.(runtime.ContainerInspectRuntime)
		if !ok {
			return nil, envelope.NewError(envelope.CodeUnsupported, "当前引擎不支持读取容器详情")
		}
		d, err := cap.InspectContainer(ctx, p.ContainerID)
		if err != nil {
			// 统一交给 toEnvelopeError 做协议码映射（与其它容器操作一致）。
			return nil, err
		}
		return toInspectResult(d), nil
	}
}

// toInspectResult 把 runtime 窄视图转成协议结构。
func toInspectResult(d runtime.InspectDetail) envelope.ContainerInspectResult {
	out := envelope.ContainerInspectResult{
		ID:             d.ID,
		Name:           d.Name,
		Image:          d.Image,
		State:          d.State,
		Status:         d.Status,
		Created:        d.Created,
		StartedAt:      d.StartedAt,
		FinishedAt:     d.FinishedAt,
		ExitCode:       d.ExitCode,
		RestartCount:   d.RestartCount,
		NetworkMode:    d.NetworkMode,
		Command:        d.Command,
		Networks:       make([]envelope.ContainerNetwork, 0, len(d.Networks)),
		Mounts:         make([]envelope.ContainerMount, 0, len(d.Mounts)),
		PublishedPorts: make([]envelope.PublishedPort, 0),
		ExposedPorts:   d.ExposedPorts,
		ResourceLimits: envelope.ContainerResourceLimits{
			MemoryBytes: d.MemoryBytes,
			NanoCPUs:    d.NanoCPUs,
			PidsLimit:   d.PidsLimit,
		},
	}
	for _, n := range d.Networks {
		out.Networks = append(out.Networks, envelope.ContainerNetwork{
			Name:        n.Name,
			IPAddress:   n.IPAddress,
			IPv6Address: n.IPv6Address,
		})
	}
	for _, m := range d.Mounts {
		out.Mounts = append(out.Mounts, envelope.ContainerMount{
			Type:        m.Type,
			Source:      m.Source,
			Destination: m.Destination,
			ReadOnly:    m.ReadOnly,
		})
	}
	// 只有 Public>0 的才进 PublishedPorts（与列表口径一致）。
	for _, p := range d.Ports {
		if p.Public <= 0 || p.Public > 65535 || p.Private <= 0 || p.Private > 65535 {
			continue
		}
		proto := p.Type
		if proto == "" {
			proto = "tcp"
		}
		out.PublishedPorts = append(out.PublishedPorts, envelope.PublishedPort{
			HostIP:        p.IP,
			HostPort:      uint16(p.Public),
			ContainerPort: uint16(p.Private),
			Protocol:      proto,
		})
	}
	return out
}
