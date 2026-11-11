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
