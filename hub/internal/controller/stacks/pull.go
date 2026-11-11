package stacks

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/controller"
	comp "arkade/hub/internal/service/compose"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"context"
)

func (c *Controller) StartPull(ctx context.Context, r *api.StartPullReq) (*api.PullRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermOperate); e != nil {
		return nil, e
	}
	var p api.PullInput
	if e := decode(ctx, &p); e != nil {
		return nil, e
	}
	if !comp.ValidTaskKey(p.RequestID) {
		return nil, controller.Failure("bad_request", "任务请求标识不合法")
	}
	if _, e := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name); e != nil {
		return nil, c.storeError(e, "编排不存在")
	}
	task, ce := c.d.Ops.ComposePullStart(ctx, r.AgentID, envelope.ComposePullStartPayload{Name: r.Name, RequestID: p.RequestID})
	if ce != nil {
		return nil, controller.ConnectionError(ce)
	}
	if task.Name != r.Name || task.TaskID == "" {
		return nil, controller.Failure("internal", "服务器返回了不匹配的镜像更新任务")
	}
	secret(ctx)
	controller.Status(ctx, 202)
	return &api.PullRes{Task: task}, nil
}
func (c *Controller) GetPull(ctx context.Context, r *api.GetPullReq) (*api.PullRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermOperate); e != nil {
		return nil, e
	}
	if !comp.ValidTaskKey(r.TaskID) {
		return nil, controller.Failure("bad_request", "任务标识不合法")
	}
	after, e := cursor(r.After)
	if e != nil {
		return nil, e
	}
	task, ce := c.d.Ops.ComposePullGet(ctx, r.AgentID, envelope.ComposePullGetPayload{TaskID: r.TaskID, After: after})
	if ce != nil {
		return nil, controller.ConnectionError(ce)
	}
	if task.Name != r.Name || task.TaskID != r.TaskID {
		return nil, controller.Failure("not_found", "镜像更新任务不存在")
	}
	secret(ctx)
	return &api.PullRes{Task: task}, nil
}
