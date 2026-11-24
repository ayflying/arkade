package agentlink

import (
	"context"

	"arkade/protocol/envelope"
)

// ExecContainer 在容器内执行一条命令。
//
// 载荷直接用协议层的 ContainerExecPayload：Hub 与 Agent 共用同一份定义，
// 字段名不可能两边不一致（历史上删卷开关就因为各写一份而静默取到 false）。
//
// 这里**不做**任何参数裁剪或默认值填充：那些属于 Agent（真正的执行方）
// 与 HTTP 层（面向用户的入参）的职责，传输层替调用方猜默认值
// 会让"到底谁决定超时"变得不可追。
func (o *Ops) ExecContainer(ctx context.Context, agentID string, p envelope.ContainerExecPayload) (envelope.ContainerExecResult, *ConnError) {
	var out envelope.ContainerExecResult
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodContainerExec, p)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
