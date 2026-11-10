package agentlink

import (
	"arkade/protocol/envelope"
	"context"
)

// 异步任务只启动固定compose pull；不会因HTTP调用结束中止已接受任务。
func (o *Ops) ComposePullStart(ctx context.Context, agent string, p envelope.ComposePullStartPayload) (envelope.ComposePullResult, *ConnError) {
	var out envelope.ComposePullResult
	raw, err := o.reg.Call(ctx, agent, envelope.MethodComposePullStart, p)
	if err != nil {
		return out, err
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
func (o *Ops) ComposePullGet(ctx context.Context, agent string, p envelope.ComposePullGetPayload) (envelope.ComposePullResult, *ConnError) {
	var out envelope.ComposePullResult
	raw, err := o.reg.Call(ctx, agent, envelope.MethodComposePullGet, p)
	if err != nil {
		return out, err
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
