// Package v1 declares the native GoFrame group-management contracts.
package v1

import "github.com/gogf/gf/v2/frame/g"

type ListReq struct {
	g.Meta `path:"/api/groups" method:"get" tags:"Groups" summary:"列出用户组"`
}
type ListRes struct {
	Groups []GroupView `json:"groups"`
}

type ListMembersReq struct {
	g.Meta  `path:"/api/groups/{groupID}/members" method:"get" tags:"Groups" summary:"列出组成员"`
	GroupID string `json:"-" in:"path"`
}
type ListMembersRes struct {
	GroupID string       `json:"group_id"`
	Members []MemberView `json:"members"`
}

type ListGrantsReq struct {
	g.Meta  `path:"/api/groups/{groupID}/grants" method:"get" tags:"Groups" summary:"列出组授权"`
	GroupID string `json:"-" in:"path"`
}
type ListGrantsRes struct {
	GroupID string      `json:"group_id"`
	Grants  []GrantView `json:"grants"`
}

type GroupView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MemberCount int    `json:"member_count"`
	GrantCount  int    `json:"grant_count"`
	CreatedAt   int64  `json:"created_at"`
}
type MemberView struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Disabled    bool   `json:"disabled"`
}
type GrantView struct {
	AgentID    string `json:"agent_id"`
	Permission string `json:"permission"`
}
