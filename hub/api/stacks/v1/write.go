package v1

import "github.com/gogf/gf/v2/frame/g"

type CreateReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks" method:"post"`
	AgentID string `json:"-" in:"path" name:"agentID"`
}
type CreateInput struct {
	Name       string `json:"name"`
	Content    string `json:"content"`
	EnvContent string `json:"env_content"`
}
type UpdateReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}" method:"put"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type UpdateInput struct {
	Content    string  `json:"content"`
	EnvContent *string `json:"env_content"`
	Version    int64   `json:"version"`
}
type DeleteReq struct {
	g.Meta        `path:"/api/agents/{agentID}/stacks/{name}" method:"delete"`
	AgentID       string `json:"-" in:"path" name:"agentID"`
	Name          string `json:"-" in:"path" name:"name"`
	RemoveVolumes string `json:"-" in:"query" name:"remove_volumes"`
}
type ActionReq struct {
	g.Meta  `path:"/api/agents/{agentID}/stacks/{name}/actions" method:"post"`
	AgentID string `json:"-" in:"path" name:"agentID"`
	Name    string `json:"-" in:"path" name:"name"`
}
type ActionInput struct {
	Action         string   `json:"action"`
	Services       []string `json:"services"`
	RemoveVolumes  bool     `json:"remove_volumes"`
	IncludeVolumes bool     `json:"include_volumes"`
}

// ActionRes is a closed union of the operation and backup response shapes.
// Only the selected branch is marshaled; no synthetic wrapper reaches the wire.
type ActionRes struct {
	Operation *OpResult
	Backup    *BackupResult
}
type BackupResult struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	SizeBytes int64    `json:"size_bytes"`
	Files     []string `json:"files"`
	CreatedAt int64    `json:"created_at"`
}
