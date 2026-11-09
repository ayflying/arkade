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

// RestartContainer 重启容器（先优雅停止再启动，容器对象保留）。
//
// 不暴露 force：重启的语义是"让它重新跑起来"，即使上一次是强杀的，
// 也不影响这一次。给重启加 force 只会让调用方纠结，没有实际收益。
func (o *Ops) RestartContainer(ctx context.Context, agentID, containerID string) (envelope.ContainerOpResult, *ConnError) {
	return o.containerOp(ctx, agentID, envelope.MethodContainerRestart, containerID, false)
}

// RemoveContainer 删除容器。
//
// 两个开关都必须由调用方显式给出，不设默认值在这里：
// force 决定运行中的容器能否被删掉，removeVolumes 是数据丢失开关。
// 在传输层替调用方"猜一个默认值"，等于把危险决定的落点藏起来。
func (o *Ops) RemoveContainer(ctx context.Context, agentID, containerID string, force, removeVolumes bool) (envelope.ContainerOpResult, *ConnError) {
	var out envelope.ContainerOpResult
	// 载荷直接用协议层的 ContainerOpPayload：
	// 这里曾经有一个本地结构体只为补 remove_volumes 字段，
	// 但那形成了两份跨模块契约（Hub 与 Agent 各一份），
	// 全靠 JSON 字段名维持一致——名字对不上就静默取零值，
	// 表现为"用户以为删了卷其实没删"，且没有任何报错。
	// 现在字段已在协议里，两侧共用同一份定义，改名会在编译期暴露。
	req := envelope.ContainerOpPayload{
		ContainerID:   containerID,
		Force:         force,
		RemoveVolumes: removeVolumes,
	}
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodContainerRemove, req)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
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
