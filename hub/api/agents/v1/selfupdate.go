package v1

import "github.com/gogf/gf/v2/frame/g"

// SelfUpdateReq 让某台服务器更新自己所在容器的镜像。
//
// ============================ 为什么没有"目标版本"参数 ============================
//
// `docker compose up` 用的是**编排文件里写的镜像 tag**，不是调用方传的值。
// 因此"更新到哪个版本"由用户自己的 compose.yml 决定：
//   - 写 `:latest` → 拉到最新；
//   - 固定 `:0.0.24` → 只会重新拉 0.0.24（版本不会变，前端据此提示用户改 tag）。
//
// 在这里加一个 tag 参数会产生"我选了 0.0.25 但它没生效"的假象，
// 比没有这个参数更糟。
type SelfUpdateReq struct {
	g.Meta  `path:"/api/agents/{agentID}/self-update" method:"post" tags:"Agents" summary:"更新服务器组件镜像"`
	AgentID string `json:"-" in:"path"`
}

// SelfUpdateRes 是受理结果，不是执行结果。
//
// 更新会把目标容器重建掉，因此**不可能**同步返回"成功/失败"。
// 前端应据 Accepted 进入"更新中"状态，再用列表轮询判断它是否带着新版本回来。
type SelfUpdateRes struct {
	// Agent 是提交时的服务器视图（用于前端立即更新版本显示）。
	Agent AgentView `json:"agent"`

	// Accepted 是否已提交重建。false 且 Blockers 为空只可能是"没有可做的更新"。
	Accepted bool `json:"accepted"`

	// Blockers 是不能自更新的固定中文原因（非空表示这个部署方式不支持）。
	Blockers []string `json:"blockers"`

	// RequestID 本次更新的幂等键，前端可用于日志与重试对齐。
	RequestID string `json:"request_id"`

	// CurrentImage 目标服务器当前运行的镜像引用。
	CurrentImage string `json:"current_image"`

	// HelperContainerName 执行重建的一次性容器名（排查时用）。
	HelperContainerName string `json:"helper_container_name"`

	// Message 后端写好的中文说明，直接展示，不要在前端重拼。
	Message string `json:"message"`
}
