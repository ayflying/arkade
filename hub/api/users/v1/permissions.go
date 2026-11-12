package v1

import "github.com/gogf/gf/v2/frame/g"

// Effective permissions are computed by the server, never inferred from UI visibility.
type PermissionsReq struct {
	g.Meta `path:"/api/users/{userID}/permissions" method:"get" tags:"Users" summary:"查看用户有效权限"`
	UserID string `json:"-" in:"path" name:"userID"`
}
type PermissionSource struct {
	GroupID    string `json:"group_id"`
	GroupName  string `json:"group_name"`
	Permission string `json:"permission"`
}
type AgentPermission struct {
	AgentID    string             `json:"agent_id"`
	AgentName  string             `json:"agent_name"`
	Permission string             `json:"permission"`
	Sources    []PermissionSource `json:"sources"`
}
type PermissionsRes struct {
	UserID   string            `json:"user_id"`
	Role     string            `json:"role"`
	Disabled bool              `json:"disabled"`
	Agents   []AgentPermission `json:"agents"`
}
