package agentlink

import (
	"arkade/protocol/envelope"
	"context"
)

func (o *Ops) ComposeOperationStart(ctx context.Context, agent string, p envelope.ComposeOperationStartPayload) (envelope.ComposeOperationResult, *ConnError) {
	var out envelope.ComposeOperationResult
	raw, err := o.reg.Call(ctx, agent, envelope.MethodComposeOperationStart, p)
	if err != nil {
		return out, err
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
func (o *Ops) ComposeOperationGet(ctx context.Context, agent string, p envelope.ComposeOperationGetPayload) (envelope.ComposeOperationResult, *ConnError) {
	var out envelope.ComposeOperationResult
	raw, err := o.reg.Call(ctx, agent, envelope.MethodComposeOperationGet, p)
	if err != nil {
		return out, err
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
