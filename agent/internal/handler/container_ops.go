package handler

// container_ops.go 承载容器的重启与删除处理器。
//
// 与 container.go 分开的理由：这两个操作有一个共同的敏感点——
// 都有"可能造成不可逆后果"的开关（force 直接杀进程、remove_volumes 删数据卷）。
// 集中在一处，将来审查危险操作时能一眼看全，也便于给每个开关写清边界。

import (
	"context"
	"encoding/json"
	"strings"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// removePayload 已删除：改用协议层的 envelope.ContainerOpPayload。
//
// 原先这里有一个本地结构体，只为多带 remove_volumes 一个字段。
// 问题是这就形成了两份跨模块契约（Hub 一份、Agent 一份），
// 全靠 JSON 字段名和注释维持一致——一旦名字对不上，
// 反序列化会静默取零值，表现为"用户以为删了卷其实没删"，且没有任何报错。
// 现在该字段已在 protocol 的 ContainerOpPayload 里，两侧共用同一份定义，
// 字段名改动会在编译期暴露。

// newRestartHandler 处理 container.restart。
//
// 语义：先把容器优雅停止再启动，容器对象本身保留（区别于 remove）。
// 返回值回读最新状态，与 start/stop 的做法一致——
// 前端不必再发一次列表请求就能刷新那一行。
func newRestartHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := opPayload(payload)
		if err != nil {
			return nil, err
		}
		if err := rt.Restart(ctx, p.ContainerID); err != nil {
			return nil, err
		}
		return opResult(ctx, rt, p.ContainerID), nil
	}
}

// newRemoveHandler 处理 container.remove。
//
// 这是全项目最危险的容器操作：容器一旦删除不可撤销，
// 其匿名卷、网络接入关系都会随之变化。
//
// 因此这里做三件事：
//  1. force 必须透传——运行中的容器不支持 force 就根本删不掉；
//  2. remove_volumes 显式解析，默认 false（删卷是数据丢失操作）；
//  3. 载荷必须是完整 JSON 对象。**这是安全边界**：如果载荷被误传成
//     一个 `true` 字面量（宽松解码下能解成 Force=true 的零值结构体），
//     语义就从"删除容器"变成了"强制删除"，静默放大破坏力。
//     要求对象形态能直接拒掉这种误调用。
func newRemoveHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		if !looksLikeJSONObject(payload) {
			return nil, badRequestf("删除容器的载荷必须是 JSON 对象，形如 {\"container_id\":\"...\",\"force\":false}")
		}
		var p envelope.ContainerOpPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, badRequestf("载荷格式错误: %v", err)
		}
		id := strings.TrimSpace(p.ContainerID)
		if id == "" {
			return nil, badRequestf("container_id 不能为空")
		}

		if err := rt.RemoveContainer(ctx, id, p.Force, p.RemoveVolumes); err != nil {
			return nil, err
		}

		// 删除成功后容器已不存在，回读状态没有意义（也必然查不到）。
		// 返回一个仅含 ID 与空状态的结果：前端据此把该行从列表移除。
		// 状态为空是刻意的，不是漏填。
		return envelope.ContainerOpResult{ContainerID: id}, nil
	}
}

// looksLikeJSONObject 判断载荷是否是 JSON 对象（而不是 null / 标量 / 数组）。
//
// 为什么不用 json.Unmarshal 到 map 再判断：那会为了一个形态检查
// 多分配一次 map。这里只跳过前导空白看首字节，够用且零分配。
func looksLikeJSONObject(payload []byte) bool {
	for _, b := range payload {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}
