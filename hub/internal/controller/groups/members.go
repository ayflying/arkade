package groups

import (
	api "arkade/hub/api/groups/v1"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/store"
	"context"
)

func (c *Controller) ListMembers(ctx context.Context, req *api.ListMembersReq) (*api.ListMembersRes, error) {
	if _, err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if _, err := c.db.GetGroup(ctx, req.GroupID); err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	members, err := c.db.ListGroupMembers(ctx, req.GroupID)
	if err != nil {
		return nil, c.storeError(err, "查询组成员失败")
	}
	return &api.ListMembersRes{GroupID: req.GroupID, Members: memberViews(members)}, nil
}

func (c *Controller) SetMembers(ctx context.Context, req *api.SetMembersReq) (*api.SetMembersRes, error) {
	if decoded, ok := httpwire.DecodedBody[api.SetMembersReq](ctx); ok {
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
	missing, err := c.db.UsersMissing(ctx, req.UserIDs)
	if err != nil {
		return nil, c.storeError(err, "查询用户是否存在失败")
	}
	if len(missing) > 0 {
		return nil, badRequest("以下用户不存在: %v", missing)
	}
	// Nil/missing/null arrays all mean clear; the store deduplicates atomically.
	if err := c.db.SetGroupMembers(ctx, req.GroupID, req.UserIDs); err != nil {
		return nil, c.storeError(err, "用户组不存在")
	}
	members, err := c.db.ListGroupMembers(ctx, req.GroupID)
	if err != nil {
		return nil, c.storeError(err, "查询组成员失败")
	}
	return &api.SetMembersRes{GroupID: req.GroupID, Members: memberViews(members)}, nil
}

func memberViews(members []store.GroupMember) []api.MemberView {
	views := make([]api.MemberView, 0, len(members))
	for _, m := range members {
		views = append(views, api.MemberView{ID: m.ID, Username: m.Username,
			DisplayName: m.DisplayName, Role: string(m.Role), Disabled: m.Disabled})
	}
	return views
}
