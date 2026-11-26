package v1

import "github.com/gogf/gf/v2/frame/g"

type PatchEnvReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/env" method:"patch"`
	AgentID string `in:"path" json:"agentID"`
	Name    string `in:"path" json:"name"`
}

type PatchEnvInput struct {
	Version int64             `json:"version"`
	Updates map[string]string `json:"updates"`
}

type PatchEnvRes struct {
	Version     int64    `json:"version"`
	ChangedKeys []string `json:"changed_keys"`
}
