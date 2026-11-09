package agentlink

// ops_compose.go 是编排与资源类操作的高层接口。
//
// 与 ops.go 同样的分层原则：HTTP 层不感知协议方法名与载荷结构，
// 只调用这里的方法。这样协议演进时改动被限制在连接层。
//
// 关于「列表类」与「动作类」的区别：
//   - 列表类（镜像/网络/卷/编排状态）失败时应当让调用方看到真实错误；
//   - 动作类（up/down/remove）除了错误还必须带回 Agent 的命令输出，
//     否则用户只能看到"失败了"，无法判断是拉不到镜像还是端口冲突。

import (
	"context"

	"arkade/protocol/envelope"
)

// ComposeUp 下发编排内容并启动。
//
// 这是「Hub → Agent 单向下发」的唯一入口：内容随请求走，Agent 落盘后执行。
// 调用方必须先写库再调它，这样即使这里失败，用户写的编排也不会丢
// （失败只影响本机执行状态，Hub 侧记录仍在，可重试下发）。
func (o *Ops) ComposeUp(ctx context.Context, agentID string, spec envelope.ComposeSpecPayload) (envelope.ComposeOpResult, *ConnError) {
	return o.callCompose(ctx, agentID, envelope.MethodComposeUp, spec)
}

// ComposeDown 停止并移除容器（保留数据卷）。
func (o *Ops) ComposeDown(ctx context.Context, agentID string, req envelope.ComposeOpPayload) (envelope.ComposeOpResult, *ConnError) {
	return o.callCompose(ctx, agentID, envelope.MethodComposeDown, req)
}

// ComposeStop 停止容器但不移除。
func (o *Ops) ComposeStop(ctx context.Context, agentID string, req envelope.ComposeOpPayload) (envelope.ComposeOpResult, *ConnError) {
	return o.callCompose(ctx, agentID, envelope.MethodComposeStop, req)
}

// ComposeRestart 重启容器。
func (o *Ops) ComposeRestart(ctx context.Context, agentID string, req envelope.ComposeOpPayload) (envelope.ComposeOpResult, *ConnError) {
	return o.callCompose(ctx, agentID, envelope.MethodComposeRestart, req)
}

// ComposePull 拉取镜像（对应界面上的"更新镜像"）。
func (o *Ops) ComposePull(ctx context.Context, agentID string, req envelope.ComposeOpPayload) (envelope.ComposeOpResult, *ConnError) {
	return o.callCompose(ctx, agentID, envelope.MethodComposePull, req)
}

// ComposeRebuild 强制重建（pull + up --force-recreate）。
func (o *Ops) ComposeRebuild(ctx context.Context, agentID string, req envelope.ComposeOpPayload) (envelope.ComposeOpResult, *ConnError) {
	return o.callCompose(ctx, agentID, envelope.MethodComposeRebuild, req)
}

// ComposeRemove 删除编排：down -v + 删除本机文件。危险操作。
func (o *Ops) ComposeRemove(ctx context.Context, agentID string, req envelope.ComposeOpPayload) (envelope.ComposeOpResult, *ConnError) {
	return o.callCompose(ctx, agentID, envelope.MethodComposeRemove, req)
}

// callCompose 是全部编排动作的唯一调用实现。
//
// 七个动作的收发流程完全一样（下发 → 解码 ComposeOpResult），
// 因此合并成一个私有方法。之所以仍然为每个动作提供具名方法，
// 是为了让 HTTP 层读起来是「调 ComposeRebuild」而不是「调 Call 带个字符串」——
// 后者一旦写错方法名，编译器帮不上忙。
func (o *Ops) callCompose(ctx context.Context, agentID, method string, payload any) (envelope.ComposeOpResult, *ConnError) {
	var out envelope.ComposeOpResult
	raw, cerr := o.reg.Call(ctx, agentID, method, payload)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ComposeList 查询 Agent 本机的编排项目与实时状态。
func (o *Ops) ComposeList(ctx context.Context, agentID string) (envelope.ComposeListResult, *ConnError) {
	var out envelope.ComposeListResult
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodComposeList, nil)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ComposeBackup 让 Agent 备份编排文件与可选的数据卷。
func (o *Ops) ComposeBackup(ctx context.Context, agentID string, req envelope.ComposeBackupPayload) (envelope.ComposeBackupResult, *ConnError) {
	var out envelope.ComposeBackupResult
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodComposeBackup, req)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ComposeLogs 拉取某个编排（或指定服务）的日志。
func (o *Ops) ComposeLogs(ctx context.Context, agentID string, req envelope.ComposeLogsPayload) (envelope.ComposeLogsResult, *ConnError) {
	var out envelope.ComposeLogsResult
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodComposeLogs, req)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ComposePS 查看编排下的容器状态。
//
// 返回的载荷结构与容器列表一致（Agent 侧直接把 compose ps 归一成容器摘要），
// 因此复用 ContainerListResult，前端可以用同一套渲染逻辑。
func (o *Ops) ComposePS(ctx context.Context, agentID string, req envelope.ComposeOpPayload) (envelope.ContainerListResult, *ConnError) {
	var out envelope.ContainerListResult
	raw, cerr := o.reg.Call(ctx, agentID, envelope.MethodComposePS, req)
	if cerr != nil {
		return out, cerr
	}
	if err := decodeInto(raw, &out); err != nil {
		return out, err
	}
	return out, nil
}

// ListImages 列出镜像。
func (o *Ops) ListImages(ctx context.Context, agentID string) (envelope.ImageListResult, *ConnError) {
	var out envelope.ImageListResult
	if cerr := o.callInto(ctx, agentID, envelope.MethodImageList, nil, &out); cerr != nil {
		return out, cerr
	}
	return out, nil
}

// RemoveImage 删除镜像。force 语义与 docker rmi -f 一致。
func (o *Ops) RemoveImage(ctx context.Context, agentID string, req envelope.ImageRemovePayload) *ConnError {
	return o.callInto(ctx, agentID, envelope.MethodImageRemove, req, nil)
}

// ListNetworks 列出网络。
func (o *Ops) ListNetworks(ctx context.Context, agentID string) (envelope.NetworkListResult, *ConnError) {
	var out envelope.NetworkListResult
	if cerr := o.callInto(ctx, agentID, envelope.MethodNetworkList, nil, &out); cerr != nil {
		return out, cerr
	}
	return out, nil
}

// RemoveNetwork 删除网络。
func (o *Ops) RemoveNetwork(ctx context.Context, agentID string, req envelope.NetworkRemovePayload) *ConnError {
	return o.callInto(ctx, agentID, envelope.MethodNetworkRemove, req, nil)
}

// ListVolumes 列出存储卷。
func (o *Ops) ListVolumes(ctx context.Context, agentID string) (envelope.VolumeListResult, *ConnError) {
	var out envelope.VolumeListResult
	if cerr := o.callInto(ctx, agentID, envelope.MethodVolumeList, nil, &out); cerr != nil {
		return out, cerr
	}
	return out, nil
}

// RemoveVolume 删除存储卷（数据丢失操作）。
func (o *Ops) RemoveVolume(ctx context.Context, agentID string, req envelope.VolumeRemovePayload) *ConnError {
	return o.callInto(ctx, agentID, envelope.MethodVolumeRemove, req, nil)
}

// callInto 是「下发 → 解码到 out」的共用实现。
//
// out 为 nil 表示只关心成功与否（删除类操作）。合并成一个方法是为了避免
// 镜像/网络/卷六个接口各写一遍相同的十行代码——这正是项目明令禁止的复制实现。
func (o *Ops) callInto(ctx context.Context, agentID, method string, payload, out any) *ConnError {
	raw, cerr := o.reg.Call(ctx, agentID, method, payload)
	if cerr != nil {
		return cerr
	}
	if out == nil {
		return nil
	}
	return decodeInto(raw, out)
}
