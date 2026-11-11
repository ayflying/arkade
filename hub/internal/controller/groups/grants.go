package groups

import (
	api "arkade/hub/api/groups/v1"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/user"
	"context"
)

func (c *Controller) ListGrants(ctx context.Context, req *api.ListGrantsReq) (*api.ListGrantsRes, error) {
	if _, err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if _, err := c.db.GetGroup(ctx, req.GroupID); err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	grants, err := c.db.ListGroupGrants(ctx, req.GroupID)
	if err != nil {
		return nil, c.storeError(err, "查询组授权失败")
	}
	return &api.ListGrantsRes{GroupID: req.GroupID, Grants: grantViews(grants)}, nil
}

func (c *Controller) SetGrants(ctx context.Context, req *api.SetGrantsReq) (*api.SetGrantsRes, error) {
	if decoded, ok := httpwire.DecodedBody[api.SetGrantsReq](ctx); ok {
		body := *decoded
		body.GroupID = req.GroupID
		req = &body
	}
	if _, err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if _, err := c.db.GetGroup(ctx, req.GroupID); err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	out := make([]user.GroupGrant, 0, len(req.Grants))
	for i, grant := range req.Grants {
		if grant.AgentID == "" {
			return nil, badRequest("第 %d 条授权缺少 agent_id", i+1)
		}
		perm := user.ParsePermission(grant.Permission)
		if perm == user.PermNone {
			return nil, badRequest("第 %d 条授权的 permission 必须是 view、operate 或 manage，实际为 %q", i+1, grant.Permission)
		}
		// Offline/unregistered agents are deliberately accepted. Input permissions
		// use the shared exact enum; none/unknown/case variants must never persist.
		out = append(out, user.GroupGrant{GroupID: req.GroupID, AgentID: grant.AgentID, Permission: perm})
	}
	if err := c.db.SetGroupGrants(ctx, req.GroupID, out); err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	saved, err := c.db.ListGroupGrants(ctx, req.GroupID)
	if err != nil {
		return nil, c.storeError(err, "查询组授权失败")
	}
	return &api.SetGrantsRes{GroupID: req.GroupID, Grants: grantViews(saved)}, nil
}

func grantViews(grants []user.GroupGrant) []api.GrantView {
	views := make([]api.GrantView, 0, len(grants))
	for _, grant := range grants {
		views = append(views, api.GrantView{AgentID: grant.AgentID, Permission: grant.Permission.String()})
	}
	return views
}
