package users

import (
	api "arkade/hub/api/users/v1"
	"arkade/hub/internal/user"
	"context"
)

// Permissions exposes both the effective level and its group provenance. A
// disabled account retains grants for administration but can exercise none.
func (c *Controller) Permissions(ctx context.Context, req *api.PermissionsReq) (*api.PermissionsRes, error) {
	if _, err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	target, err := c.db.GetUserByID(ctx, req.UserID)
	if err != nil {
		return nil, c.storeError(err, "用户不存在")
	}
	agents, err := c.db.ListAgents(ctx)
	if err != nil {
		return nil, c.storeError(err, "查询服务器失败")
	}
	groups, err := c.db.ListGroupsByUser(ctx, target.ID)
	if err != nil {
		return nil, c.storeError(err, "查询用户组失败")
	}
	names := make(map[string]string, len(groups))
	for _, group := range groups {
		names[group.ID] = group.Name
	}
	grants, err := c.db.ListUserGrants(ctx, target.ID)
	if err != nil {
		return nil, c.storeError(err, "查询用户权限失败")
	}
	out := &api.PermissionsRes{UserID: target.ID, Role: string(target.Role), Disabled: target.Disabled, Agents: make([]api.AgentPermission, 0, len(agents))}
	for _, agent := range agents {
		level := user.PermNone
		view := api.AgentPermission{AgentID: agent.ID, AgentName: agent.Name, Sources: []api.PermissionSource{}}
		for _, grant := range grants {
			if grant.AgentID != agent.ID {
				continue
			}
			view.Sources = append(view.Sources, api.PermissionSource{GroupID: grant.GroupID, GroupName: names[grant.GroupID], Permission: grant.Permission.String()})
			if grant.Permission > level {
				level = grant.Permission
			}
		}
		if target.Role == user.RoleAdmin {
			level = user.PermManage
		}
		if target.Disabled {
			level = user.PermNone
		}
		view.Permission = level.String()
		out.Agents = append(out.Agents, view)
	}
	return out, nil
}
