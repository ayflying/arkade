// Package v1 declares native resource API contracts.
package v1

import (
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/frame/g"
)

type ListContainersReq struct {
	g.Meta  `path:"/api/agents/{agentID}/containers" method:"get" tags:"Resources"`
	AgentID string `json:"-" in:"path"`
}
type ListContainersRes struct {
	AgentID    string                      `json:"agent_id"`
	Containers []envelope.ContainerSummary `json:"containers"`
}
type ContainerDetailReq struct {
	g.Meta      `path:"/api/agents/{agentID}/containers/{containerID}" method:"get" tags:"Resources"`
	AgentID     string `json:"-" in:"path"`
	ContainerID string `json:"-" in:"path"`
}
type ContainerDetailRes struct {
	Container envelope.ContainerInspectResult `json:"container"`
}
type ContainerStartReq struct {
	g.Meta      `path:"/api/agents/{agentID}/containers/{containerID}/start" method:"post" tags:"Resources"`
	AgentID     string `json:"-" in:"path"`
	ContainerID string `json:"-" in:"path"`
}
type ContainerStopReq struct {
	g.Meta      `path:"/api/agents/{agentID}/containers/{containerID}/stop" method:"post" tags:"Resources"`
	AgentID     string `json:"-" in:"path"`
	ContainerID string `json:"-" in:"path"`
}
type ContainerRestartReq struct {
	g.Meta      `path:"/api/agents/{agentID}/containers/{containerID}/restart" method:"post" tags:"Resources"`
	AgentID     string `json:"-" in:"path"`
	ContainerID string `json:"-" in:"path"`
}
type ContainerRemoveReq struct {
	g.Meta        `path:"/api/agents/{agentID}/containers/{containerID}" method:"delete" tags:"Resources"`
	AgentID       string `json:"-" in:"path"`
	ContainerID   string `json:"-" in:"path"`
	Force         string `json:"force" in:"query"`
	RemoveVolumes string `json:"remove_volumes" in:"query"`
}
type ContainerOpRes struct {
	ContainerID string `json:"container_id"`
	State       string `json:"state"`
}
type ContainerStartRes = ContainerOpRes
type ContainerStopRes = ContainerOpRes
type ContainerRestartRes = ContainerOpRes
type ContainerRemoveRes = ContainerOpRes

type ContainerRecreateReq struct {
	g.Meta      `path:"/api/agents/{agentID}/containers/{containerID}/recreate" method:"post" tags:"Resources"`
	AgentID     string `json:"-" in:"path"`
	ContainerID string `json:"-" in:"path"`
	// Pull 为 true 时先拉取镜像最新版本再重建。默认 false：拉镜像耗时且改变运行版本，
	// 必须由用户显式勾选。用字符串接收再解析，与 force / remove_volumes 的处理保持一致。
	Pull string `json:"pull" in:"query"`
}
type ContainerRecreateRes = ContainerOpRes

type ContainerLogsReq struct {
	g.Meta      `path:"/api/agents/{agentID}/containers/{containerID}/logs" method:"get" tags:"Resources"`
	AgentID     string `json:"-" in:"path"`
	ContainerID string `json:"-" in:"path"`
	Tail        string `json:"tail" in:"query"`
}
type ContainerLogsRes struct {
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"`
}

// ContainerExecReq 在容器内执行一条命令（一次性，非交互）。
//
// 为什么用请求体而不是 query：命令向量里可能有空格、引号、分号，
// 塞进 URL 既要多次编码，又会把命令全文写进访问日志。
// 用 JSON 体是唯一能把"参数边界"表达清楚的方式。
type ContainerExecReq struct {
	g.Meta      `path:"/api/agents/{agentID}/containers/{containerID}/exec" method:"post" tags:"Resources"`
	AgentID     string `json:"-" in:"path"`
	ContainerID string `json:"-" in:"path"`
}
type ContainerExecRes struct {
	Result envelope.ContainerExecResult `json:"result"`
}
