// Package agentlink 是 Hub 侧 Agent 连接的编排层。
package agentlink

// ops.go 是容器操作的高层接口，供 HTTP 层直接调用。
//
// 设计原因：HTTP 层不应该知道协议方法名与载荷结构，
// 这些细节属于连接层；这样即使协议版本变化，HTTP 层也不需要改动。

import (
	"context"

	"arkade/protocol/envelope"
)

// Ops 提供容器操作。它只依赖 Registry，与存储层无关。
type Ops struct {
	reg *Registry
}

// NewOps 构造操作入口。
func NewOps(reg *Registry) *Ops { return &Ops{reg: reg} }

// ListContainers 查询某台 Agent 上的容器列表。
func (o *Ops) ListContainers(ctx context.Context, agentID string) (envelope.ContainerListResult, *ConnError) {
	var out envelope.ContainerListResult
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodContainerList, nil)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

// StartContainer 启动容器。
func (o *Ops) StartContainer(ctx context.Context, agentID, containerID string) (envelope.ContainerOpResult, *ConnError) {
	return o.containerOp(ctx, agentID, envelope.MethodContainerStart, containerID, false)
}

// StopContainer 停止容器。force=false 表示先用 SIGTERM 优雅退出，
// 这是更安全的默认值——强杀可能丢数据。
func (o *Ops) StopContainer(ctx context.Context, agentID, containerID string) (envelope.ContainerOpResult, *ConnError) {
	return o.containerOp(ctx, agentID, envelope.MethodContainerStop, containerID, false)
}

// containerOp 是启停共用的实现，避免两处重复写相同的调用与解析流程。
func (o *Ops) containerOp(ctx context.Context, agentID, method, containerID string, force bool) (envelope.ContainerOpResult, *ConnError) {
	var out envelope.ContainerOpResult
	req := envelope.ContainerOpPayload{ContainerID: containerID, Force: force}

	raw, cerr := o.reg.Call(ctx, agentID, method, req)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

// Logs 拉取容器日志。行数上限由 Agent 强制，这里只传请求。
func (o *Ops) Logs(ctx context.Context, agentID, containerID string, tail int) (envelope.ContainerLogsResult, *ConnError) {
	var out envelope.ContainerLogsResult
	req := envelope.ContainerLogsPayload{ContainerID: containerID, TailLines: tail}

	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodContainerLogs, req)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}
