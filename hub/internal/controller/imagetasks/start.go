package imagetasks

import (
	"context"
	"strings"

	api "arkade/hub/api/imagetasks/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/httpwire"
	"arkade/hub/internal/user"
)

func (c *Controller) start(ctx context.Context, kind, agent, target, reference string, confirmation bool) (api.TaskView, error) {
	if err := c.access.Require(ctx, agent, user.PermManage); err != nil {
		return api.TaskView{}, err
	}
	reference = strings.TrimSpace(reference)
	if len(reference) == 0 || len(reference) > 512 || strings.ContainsAny(reference, "\r\n\x00") {
		return api.TaskView{}, badRequest("请提供有效镜像引用")
	}
	if kind == "migrate" {
		if !confirmation || target == "" || target == agent {
			return api.TaskView{}, badRequest("请选择不同的目标服务器并确认迁移")
		}
		if err := c.access.Require(ctx, target, user.PermManage); err != nil {
			return api.TaskView{}, err
		}
	}
	if !c.deps.Registry.IsOnline(agent) {
		return api.TaskView{}, controller.Failure("agent_offline", "源服务器离线")
	}
	if kind == "migrate" && !c.deps.Registry.IsOnline(target) {
		return api.TaskView{}, controller.Failure("agent_offline", "目标服务器离线")
	}
	u, err := c.current(ctx)
	if err != nil {
		return api.TaskView{}, err
	}
	t, err := c.deps.Tasks.Start(kind, agent, target, reference, u.ID)
	if err != nil {
		return api.TaskView{}, badRequest("无法创建镜像任务，请检查并发任务数与输入")
	}
	accepted(ctx)
	return api.View(t), nil
}

// decode checks source access before touching JSON and never trusts GF-bound query fields.
func (c *Controller) decode(ctx context.Context, agent string, target any) error {
	if err := c.access.Require(ctx, agent, user.PermManage); err != nil {
		return err
	}
	if httpwire.HasRawBody(ctx) {
		if err := httpwire.DecodeJSON(httpwire.RestoreRawBody(ctx), target); err != nil {
			return badRequest("请求内容无效")
		}
	}
	return nil
}

func (c *Controller) Pull(ctx context.Context, req *api.PullReq) (*api.PullRes, error) {
	if httpwire.HasRawBody(ctx) {
		body := api.PullReq{AgentID: req.AgentID}
		if err := c.decode(ctx, req.AgentID, &body); err != nil {
			return nil, err
		}
		req = &body
	}
	if decoded, ok := httpwire.DecodedBody[api.PullReq](ctx); ok {
		body := *decoded
		body.AgentID = req.AgentID
		req = &body
	}
	t, err := c.start(ctx, "pull", req.AgentID, req.TargetAgentID, req.Reference, req.Confirmation)
	if err != nil {
		return nil, err
	}
	return &api.PullRes{Task: t}, nil
}
func (c *Controller) Export(ctx context.Context, req *api.ExportReq) (*api.ExportRes, error) {
	if httpwire.HasRawBody(ctx) {
		body := api.ExportReq{AgentID: req.AgentID}
		if err := c.decode(ctx, req.AgentID, &body); err != nil {
			return nil, err
		}
		req = &body
	}
	if decoded, ok := httpwire.DecodedBody[api.ExportReq](ctx); ok {
		body := *decoded
		body.AgentID = req.AgentID
		req = &body
	}
	t, err := c.start(ctx, "export", req.AgentID, req.TargetAgentID, req.Reference, req.Confirmation)
	if err != nil {
		return nil, err
	}
	return &api.ExportRes{Task: t}, nil
}
func (c *Controller) Migrate(ctx context.Context, req *api.MigrateReq) (*api.MigrateRes, error) {
	if httpwire.HasRawBody(ctx) {
		body := api.MigrateReq{AgentID: req.AgentID}
		if err := c.decode(ctx, req.AgentID, &body); err != nil {
			return nil, err
		}
		req = &body
	}
	if decoded, ok := httpwire.DecodedBody[api.MigrateReq](ctx); ok {
		body := *decoded
		body.AgentID = req.AgentID
		req = &body
	}
	t, err := c.start(ctx, "migrate", req.AgentID, req.TargetAgentID, req.Reference, req.Confirmation)
	if err != nil {
		return nil, err
	}
	return &api.MigrateRes{Task: t}, nil
}
