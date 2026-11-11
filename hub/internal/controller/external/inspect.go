package external

import (
	"context"
	"errors"

	api "arkade/hub/api/external/v1"
	stacks "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/controller"
	compose "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

func (c *Controller) Detail(ctx context.Context, req *api.DetailReq) (*api.DetailRes, error) {
	p, err := c.requireExternal(ctx, req.AgentID, req.Name, user.PermView)
	if err != nil {
		return nil, err
	}
	list, cerr := c.deps.Ops.ListContainers(ctx, req.AgentID)
	if cerr != nil {
		return nil, controller.ConnectionError(cerr)
	}
	containers := make([]envelope.ContainerSummary, 0)
	for _, item := range list.Containers {
		if item.StackID == req.Name {
			containers = append(containers, item)
		}
	}
	view := stacks.ExternalStackView{Name: req.Name, Status: p.Status, Services: p.Services, Running: p.Running, Total: p.Total, Managed: p.Managed}
	if c.deps.AutoAdoptState != nil {
		view.AutoAdoptStatus, view.AutoAdoptReason = c.deps.AutoAdoptState(req.AgentID, req.Name)
	}
	return &api.DetailRes{Project: view, Containers: containers}, nil
}

func (c *Controller) Config(ctx context.Context, req *api.ConfigReq) (*api.ConfigRes, error) {
	if _, err := c.requireExternal(ctx, req.AgentID, req.Name, user.PermManage); err != nil {
		return nil, err
	}
	out := &api.ConfigRes{Reason: "当前无法读取原始配置；可查看容器和日志，不会把读取失败当成空文件"}
	source, cerr := c.deps.Ops.ComposeSourceRaw(ctx, req.AgentID, req.Name)
	if cerr == nil {
		if source.Name == req.Name && source.ConfigHash == compose.ProjectHash(source.Content, "") && compose.ValidateContent(source.Content) == nil {
			out.Readable, out.Reason = true, ""
			out.Content, out.ConfigHash, out.Blockers = &source.Content, &source.ConfigHash, &source.Blockers
		} else if len(source.Blockers) > 0 {
			out.Reason = compose.SafeAutoReason(source.Blockers)
		}
	} else {
		out.Reason = "读取配置失败，请检查项目来源、文件权限及Agent状态"
	}
	if draft, err := c.deps.DB.GetExternalComposeDraft(ctx, req.AgentID, req.Name); err == nil {
		out.Draft = &draft
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, c.storeError(err, "读取草稿失败")
	}
	secret(ctx)
	return out, nil
}
