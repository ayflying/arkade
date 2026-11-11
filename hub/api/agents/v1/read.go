// Package v1 declares native GoFrame agent-management contracts.
package v1

import "github.com/gogf/gf/v2/frame/g"

type ListReq struct {
	g.Meta `path:"/api/agents" method:"get" tags:"Agents" summary:"服务器列表"`
}
type ListRes struct {
	Agents []AgentView `json:"agents"`
}

type AgentView struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	DisplayName    string            `json:"display_name"`
	Note           string            `json:"note"`
	DirectAddress  string            `json:"direct_address"`
	Disabled       bool              `json:"disabled"`
	Status         string            `json:"status"`
	Version        string            `json:"version"`
	Runtime        string            `json:"runtime"`
	RuntimeVersion string            `json:"runtime_version"`
	LastSeen       int64             `json:"last_seen"`
	Labels         map[string]string `json:"labels"`
}
