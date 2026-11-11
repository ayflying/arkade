package imagetasks

import (
	"context"
	"errors"

	api "arkade/hub/api/imagetasks/v1"
	"arkade/hub/internal/controller"
)

func (c *Controller) List(ctx context.Context, req *api.ListReq) (*api.ListRes, error) {
	if _, err := c.current(ctx); err != nil {
		return nil, err
	}
	items := make([]api.TaskView, 0)
	for _, t := range c.deps.Tasks.List() {
		if req.AgentID != "" && t.AgentID != req.AgentID && t.TargetAgentID != req.AgentID {
			continue
		}
		if err := c.allowed(ctx, t); err != nil {
			// Listing hides inaccessible entries; dependency failures are not mistaken for grants.
			var business *controller.Error
			if errors.As(err, &business) && business.Status >= 500 {
				return nil, err
			}
			continue
		}
		items = append(items, api.View(t))
	}
	secret(ctx)
	return &api.ListRes{Tasks: items}, nil
}

func (c *Controller) Get(ctx context.Context, req *api.GetReq) (*api.GetRes, error) {
	t, err := c.task(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	secret(ctx)
	return &api.GetRes{Task: api.View(t)}, nil
}

func (c *Controller) Cancel(ctx context.Context, req *api.CancelReq) (*api.CancelRes, error) {
	t, err := c.task(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	t, err = c.deps.Tasks.Cancel(t.ID)
	if err != nil {
		return nil, badRequest("任务不能取消")
	}
	return &api.CancelRes{Task: api.View(t)}, nil
}
