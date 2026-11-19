package handler

// container_recreate.go：container.recreate 处理器。
//
// 与 container_ops.go 分开的理由：重建是**唯一会销毁并重建容器对象**的操作
// （ID 会变、日志断开、匿名卷以外的容器内数据全部重来）。把它单独成文，
// 便于审查"破坏性操作"时一眼看到边界，也不会被启停这类低风险动作淹没。

import (
	"context"
	"strings"
	"time"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// recreateTimeout 是单次重建的总时限。
//
// 为什么需要它：勾选"重新拉取镜像"时，重建可能包含一次完整的镜像下载。
// Hub 侧的等待上限可能比拉取更早到期，若不给 Agent 自己的兜底时限，
// 极端情况下会出现"Hub 早已认为失败、Agent 还在慢慢建"的错位。
// 15 分钟足够拉完常见镜像，也能在最坏情况下让操作最终收敛。
const recreateTimeout = 15 * time.Minute

// newRecreateHandler 处理 container.recreate。
//
// 语义：按原配置重建容器——停旧容器、改名让位、用同名新建、启动、删旧。
// 返回值里的 ContainerID 是**新容器 ID**，不是请求里传进去的那个：
// 重建会换掉容器对象，前端必须用新 ID 才能继续操作它。
func newRecreateHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		// 与删除一致，要求载荷是 JSON 对象：宽松解码下把 `true` 之类
		// 的字面量当成结构体零值会让"重建"悄悄变成"用默认配置新建"，
		// 那会把容器配置改掉且没有任何提示。
		if !looksLikeJSONObject(payload) {
			return nil, badRequestf("重建容器的载荷必须是 JSON 对象，形如 {\"container_id\":\"...\",\"pull\":false}")
		}
		var p envelope.ContainerRecreatePayload
		if err := decodePayloadInto(payload, &p); err != nil {
			return nil, err
		}
		id := strings.TrimSpace(p.ContainerID)
		if id == "" {
			return nil, badRequestf("container_id 不能为空")
		}

		// 可选能力：引擎不支持时返回 unsupported（与容器详情同一套做法），
		// 让 Hub 能提示"请升级 Agent"，而不是一个语焉不详的运行时失败。
		cap, ok := rt.(runtime.ContainerRecreateRuntime)
		if !ok {
			return nil, envelope.NewError(envelope.CodeUnsupported, "当前引擎不支持重建容器")
		}

		opCtx, cancel := context.WithTimeout(ctx, recreateTimeout)
		defer cancel()
		newID, err := cap.RecreateContainer(opCtx, id, p.Pull)
		if err != nil {
			// 统一交给 toEnvelopeError 映射协议错误码（与其它容器操作一致）。
			return nil, err
		}
		return opResult(ctx, rt, newID), nil
	}
}
