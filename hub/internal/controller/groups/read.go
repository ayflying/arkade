package groups

import (
	api "arkade/hub/api/groups/v1"
	"context"
)

func (c *Controller) List(ctx context.Context, _ *api.ListReq) (*api.ListRes, error) {
	if _, err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	records, err := c.db.ListGroups(ctx)
	if err != nil {
		return nil, c.storeError(err, "查询用户组列表失败")
	}
	views := make([]api.GroupView, 0, len(records))
	for _, rec := range records {
		views = append(views, groupView(rec))
	}
	return &api.ListRes{Groups: views}, nil
}
