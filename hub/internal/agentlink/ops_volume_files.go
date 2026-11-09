package agentlink

import (
	"arkade/protocol/envelope"
	"context"
)

func (o *Ops) VolumeFiles(ctx context.Context, agentID string, p envelope.VolumeFilesPayload) (envelope.VolumeFilesResult, *ConnError) {
	var out envelope.VolumeFilesResult
	raw, err := o.reg.Call(ctx, agentID, envelope.MethodVolumeFiles, p)
	if err != nil {
		return out, err
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
