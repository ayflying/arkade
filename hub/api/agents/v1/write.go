package v1

import "github.com/gogf/gf/v2/frame/g"

type PatchReq struct {
	g.Meta        `path:"/api/agents/{agentID}" method:"patch" tags:"Agents" summary:"修改服务器"`
	AgentID       string             `json:"-" in:"path"`
	DirectAddress *string            `json:"direct_address"`
	DisplayName   *string            `json:"display_name"`
	Note          *string            `json:"note"`
	Labels        *map[string]string `json:"labels"`
}
type PatchRes struct {
	Agent AgentView `json:"agent"`
}
type DeleteReq struct {
	g.Meta  `path:"/api/agents/{agentID}" method:"delete" tags:"Agents" summary:"删除服务器登记"`
	AgentID string `json:"-" in:"path"`
}
type DeleteRes struct {
	Deleted      bool   `json:"deleted"`
	AgentID      string `json:"agent_id"`
	OrphanStacks int    `json:"orphan_stacks"`
	Message      string `json:"message"`
}
type DisableReq struct {
	g.Meta  `path:"/api/agents/{agentID}/disable" method:"post" tags:"Agents" summary:"禁用服务器"`
	AgentID string `json:"-" in:"path"`
}
type DisableRes struct {
	Agent AgentView `json:"agent"`
}
type EnableReq struct {
	g.Meta  `path:"/api/agents/{agentID}/enable" method:"post" tags:"Agents" summary:"启用服务器"`
	AgentID string `json:"-" in:"path"`
}
type EnableRes struct {
	Agent AgentView `json:"agent"`
}
