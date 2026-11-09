package agentlink

import (
	"arkade/protocol/envelope"
	"context"
)

// ComposePreview只执行安全预检，不修改容器或管理区文件。
func (o *Ops) ComposePreview(ctx context.Context, agentID string, req envelope.ComposePreviewPayload) (envelope.ComposePreviewResult, *ConnError) {
	var out envelope.ComposePreviewResult
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodComposePreview, req)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ComposeAdopt仅将用户确认的项目配置复制到管理区，绝不启动或重建服务。
func (o *Ops) ComposeAdopt(ctx context.Context, agentID string, req envelope.ComposeAdoptPayload) (envelope.ComposeAdoptResult, *ConnError) {
	var out envelope.ComposeAdoptResult
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodComposeAdopt, req)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
