package v1

import (
	"encoding/json"
	"github.com/gogf/gf/v2/frame/g"
)

type CreateReq struct {
	g.Meta      `path:"/api/groups" method:"post" tags:"Groups" summary:"创建用户组"`
	Name        string `json:"name"`
	Description string `json:"description"`
}
type CreateRes struct {
	Group GroupView `json:"group"`
}

type UpdateReq struct {
	g.Meta      `path:"/api/groups/{groupID}" method:"patch" tags:"Groups" summary:"修改用户组"`
	GroupID     string           `json:"-" in:"path"`
	Name        Optional[string] `json:"name"`
	Description Optional[string] `json:"description"`
}
type UpdateRes struct {
	Group GroupView `json:"group"`
}

type DeleteReq struct {
	g.Meta  `path:"/api/groups/{groupID}" method:"delete" tags:"Groups" summary:"删除用户组"`
	GroupID string `json:"-" in:"path"`
}
type DeleteRes struct{}

type SetMembersReq struct {
	g.Meta  `path:"/api/groups/{groupID}/members" method:"put" tags:"Groups" summary:"整体设置组成员"`
	GroupID string   `json:"-" in:"path"`
	UserIDs []string `json:"user_ids"`
}
type SetMembersRes struct {
	GroupID string       `json:"group_id"`
	Members []MemberView `json:"members"`
}

type SetGrantsReq struct {
	g.Meta  `path:"/api/groups/{groupID}/grants" method:"put" tags:"Groups" summary:"整体设置组授权"`
	GroupID string       `json:"-" in:"path"`
	Grants  []GrantInput `json:"grants"`
}
type SetGrantsRes struct {
	GroupID string      `json:"group_id"`
	Grants  []GrantView `json:"grants"`
}
type GrantInput struct {
	AgentID    string `json:"agent_id"`
	Permission string `json:"permission"`
}

// Optional distinguishes an omitted PATCH field from an explicit null/zero value.
// Null means present with the zero value, matching the legacy JSON contract.
type Optional[T any] struct {
	Value T
	Set   bool
}

// UnmarshalValue prevents GF's generic struct conversion from coercing values
// or reflecting directly into the optional wrapper.
func (o *Optional[T]) UnmarshalValue(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return o.UnmarshalJSON(data)
}

func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		var zero T
		o.Value = zero
		return nil
	}
	var value T
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	o.Value = value
	return nil
}
