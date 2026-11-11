package v1

import "github.com/gogf/gf/v2/frame/g"

type PullReq struct {
	g.Meta        `path:"/api/agents/{agentID}/images/pull" method:"post" tags:"ImageTasks" summary:"拉取镜像"`
	AgentID       string `json:"-" in:"path"`
	Reference     string `json:"reference"`
	TargetAgentID string `json:"target_agent_id"`
	Confirmation  bool   `json:"confirmation"`
}
type PullRes struct {
	Task TaskView `json:"task"`
}

type ExportReq struct {
	g.Meta        `path:"/api/agents/{agentID}/images/export" method:"post" tags:"ImageTasks" summary:"导出镜像"`
	AgentID       string `json:"-" in:"path"`
	Reference     string `json:"reference"`
	TargetAgentID string `json:"target_agent_id"`
	Confirmation  bool   `json:"confirmation"`
}
type ExportRes struct {
	Task TaskView `json:"task"`
}

type MigrateReq struct {
	g.Meta        `path:"/api/agents/{agentID}/images/migrate" method:"post" tags:"ImageTasks" summary:"迁移镜像"`
	AgentID       string `json:"-" in:"path"`
	Reference     string `json:"reference"`
	TargetAgentID string `json:"target_agent_id"`
	Confirmation  bool   `json:"confirmation"`
}
type MigrateRes struct {
	Task TaskView `json:"task"`
}

// ImportReq has no archive field: the transport must never parse or buffer its body.
type ImportReq struct {
	g.Meta  `path:"/api/agents/{agentID}/images/import" method:"post" tags:"ImageTasks" summary:"流式导入镜像" mime:"application/octet-stream" raw:"true"`
	AgentID string `json:"-" in:"path"`
}
type ImportRes struct {
	Task TaskView `json:"task"`
}
