package users

import (
	"context"

	api "arkade/hub/api/users/v1"
)

func (c *Controller) List(ctx context.Context, _ *api.ListReq) (*api.ListRes, error) {
	if _, err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	users, err := c.db.ListUsers(ctx)
	if err != nil {
		return nil, c.storeError(err, "查询用户列表失败")
	}
	groups, err := c.db.ListUserGroupIDs(ctx)
	if err != nil {
		return nil, c.storeError(err, "查询用户所属组失败")
	}
	views := make([]api.UserView, 0, len(users))
	for _, u := range users {
		views = append(views, viewOf(u, groups[u.ID]))
	}
	return &api.ListRes{Users: views}, nil
}
