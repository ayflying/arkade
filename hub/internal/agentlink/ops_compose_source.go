package agentlink

import (
	"arkade/protocol/envelope"
	"context"
)

// ComposeSource只读取引擎标签声明的项目配置；不写工作区或运行服务。
func (o *Ops) ComposeSource(ctx context.Context, agentID, name string) (envelope.ComposeSourceResult, *ConnError) {
	var out envelope.ComposeSourceResult
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodComposeSource, envelope.ComposeSourcePayload{Name: name})
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
