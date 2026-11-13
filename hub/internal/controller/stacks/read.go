package stacks

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/controller"
	comp "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"context"
	"errors"
	"strconv"
)

func (c *Controller) List(ctx context.Context, r *api.ListReq) (*api.ListRes, error) {
	if e := c.check(ctx, r.AgentID, "", user.PermView); e != nil {
		return nil, e
	}
	records, e := c.d.DB.ListComposeStacks(ctx, r.AgentID)
	if e != nil {
		return nil, c.storeError(e, "查询编排列表失败")
	}
	remote := map[string]envelope.ComposeProjectSummary{}
	external := []envelope.ComposeProjectSummary{}
	list, ce := c.d.Ops.ComposeList(ctx, r.AgentID)
	online := ce == nil
	if online {
		for _, p := range list.Projects {
			remote[p.Name] = p
		}
		external = list.External
	}
	out := &api.ListRes{AgentID: r.AgentID, Stacks: make([]api.StackView, 0, len(records)), AgentOnline: online}
	known := map[string]struct{}{}
	for _, rec := range records {
		known[rec.Name] = struct{}{}
		var rp *envelope.ComposeProjectSummary
		if p, ok := remote[rec.Name]; ok {
			rp = &p
		}
		v := comp.StackViewOf(rec, rp)
		c.syncHint(rec, &v)
		out.Stacks = append(out.Stacks, v)
	}
	if online {
		views := comp.ExternalViews(external, known)
		if c.d.AutoState != nil {
			for i := range views {
				views[i].AutoAdoptStatus, views[i].AutoAdoptReason, _ = c.d.AutoState(r.AgentID, views[i].Name)
			}
		}
		out.ExternalStacks = &views
	}
	return out, nil
}
func (c *Controller) syncHint(rec store.ComposeStack, v *api.StackView) {
	if c.d.AutoState != nil && rec.SyncedAt.IsZero() {
		_, reason, ok := c.d.AutoState(rec.AgentID, rec.Name)
		if ok {
			v.SyncHint = reason
		}
	}
}
func (c *Controller) Detail(ctx context.Context, r *api.DetailReq) (*api.DetailRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermView); e != nil {
		return nil, e
	}
	rec, e := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name)
	var v api.StackView
	if errors.Is(e, store.ErrNotFound) {
		list, ce := c.d.Ops.ComposeList(ctx, r.AgentID)
		if ce != nil {
			return nil, controller.ConnectionError(ce)
		}
		var p *envelope.ComposeProjectSummary
		for _, item := range append(list.External, list.Projects...) {
			if item.Name == r.Name {
				x := item
				p = &x
				break
			}
		}
		if p == nil {
			return nil, controller.Failure("not_found", "引擎中未找到该编排项目")
		}
		v = api.StackView{AgentID: r.AgentID, Name: r.Name, Managed: false, Status: p.Status, Services: p.Services, Running: p.Running, Total: p.Total}
		if v.Services == nil {
			v.Services = []string{}
		}
		if c.d.AutoState != nil {
			v.AutoAdoptStatus, v.AutoAdoptReason, _ = c.d.AutoState(r.AgentID, r.Name)
		}
	} else if e != nil {
		return nil, c.storeError(e, "编排不存在")
	} else {
		var rp *envelope.ComposeProjectSummary
		if list, ce := c.d.Ops.ComposeList(ctx, r.AgentID); ce == nil {
			for _, p := range list.Projects {
				if p.Name == r.Name {
					x := p
					rp = &x
					break
				}
			}
		}
		v = comp.StackViewOf(rec, rp)
		c.syncHint(rec, &v)
	}
	out := &api.DetailRes{Stack: v, Containers: []envelope.ContainerSummary{}}
	list, ce := c.d.Ops.ListContainers(ctx, r.AgentID)
	if ce != nil {
		out.ContainersError = "暂时无法读取容器列表，请检查服务器连接后刷新"
	} else {
		for _, item := range list.Containers {
			if item.StackID == r.Name {
				out.Containers = append(out.Containers, item)
			}
		}
	}
	return out, nil
}
func (c *Controller) Config(ctx context.Context, r *api.ConfigReq) (*api.ConfigRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermManage); e != nil {
		return nil, e
	}
	rec, e := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name)
	if e != nil {
		return nil, c.storeError(e, "编排不存在")
	}
	secret(ctx)
	return &api.ConfigRes{Content: rec.Content, EnvContent: rec.EnvContent, Version: rec.Version}, nil
}
func (c *Controller) Logs(ctx context.Context, r *api.LogsReq) (*api.LogsRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermView); e != nil {
		return nil, e
	}
	if _, e := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name); e != nil {
		return nil, c.storeError(e, "编排不存在")
	}
	if r.Service != "" && !comp.ValidName(r.Service) {
		return nil, controller.Failure("bad_request", "服务名不合法: "+strconv.Quote(r.Service))
	}
	tail := 200
	if r.Tail != "" {
		n, e := strconv.Atoi(r.Tail)
		if e != nil || n < 1 || n > 2000 {
			return nil, controller.Failure("bad_request", "tail 必须是 1 到 2000 之间的整数")
		}
		tail = n
	}
	res, ce := c.d.Ops.ComposeLogs(ctx, r.AgentID, envelope.ComposeLogsPayload{Name: r.Name, Service: r.Service, TailLines: tail})
	if ce != nil {
		return nil, controller.ConnectionError(ce)
	}
	if res.Lines == nil {
		res.Lines = []string{}
	}
	return &api.LogsRes{Lines: res.Lines, Truncated: res.Truncated}, nil
}
func (c *Controller) PS(ctx context.Context, r *api.PSReq) (*api.PSRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermView); e != nil {
		return nil, e
	}
	if _, e := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name); e != nil {
		return nil, c.storeError(e, "编排不存在")
	}
	res, ce := c.d.Ops.ComposePS(ctx, r.AgentID, envelope.ComposeOpPayload{Name: r.Name})
	if ce != nil {
		return nil, controller.ConnectionError(ce)
	}
	if res.Containers == nil {
		res.Containers = []envelope.ContainerSummary{}
	}
	return &api.PSRes{AgentID: r.AgentID, Containers: res.Containers}, nil
}
