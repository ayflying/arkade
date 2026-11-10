package agentlink

import (
	"arkade/protocol/envelope"
	"context"
)

// InspectContainer仅请求Agent白名单详情，不传递daemon原始inspect结果。
func (o *Ops) InspectContainer(ctx context.Context, agentID, containerID string) (envelope.ContainerInspectResult, *ConnError) {
	var out envelope.ContainerInspectResult
	raw, err := o.reg.Call(ctx, agentID, envelope.MethodContainerInspect, envelope.ContainerInspectPayload{ContainerID: containerID})
	if err != nil {
		return out, err
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
