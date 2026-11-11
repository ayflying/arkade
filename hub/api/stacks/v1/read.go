package v1

import "github.com/gogf/gf/v2/frame/g"

type ListReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks" method:"get"`
	AgentID string `json:"-" in:"path" name:"agentID"`
}
type DetailReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}" method:"get"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type ConfigReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/config" method:"get"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type LogsReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/logs" method:"get"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
	Service string `json:"-" in:"query" name:"service"`
	Tail    string `json:"-" in:"query" name:"tail"`
}
type PSReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/ps" method:"get"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
