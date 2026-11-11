package stacks

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/controller"
	comp "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"context"
	"strings"
	"time"
)

func (c *Controller) Create(ctx context.Context, r *api.CreateReq) (*api.SaveRes, error) {
	if e := c.check(ctx, r.AgentID, "", user.PermManage); e != nil {
		return nil, e
	}
	var p api.CreateInput
	if e := decode(ctx, &p); e != nil {
		return nil, e
	}
	for _, e := range []error{comp.ValidateName(p.Name), comp.ValidateContent(p.Content), comp.ValidateEnv(p.EnvContent)} {
		if e != nil {
			return nil, controller.Failure("bad_request", e.Error())
		}
	}
	now := time.Now().UTC()
	rec := store.ComposeStack{ID: randomID(), AgentID: r.AgentID, Name: p.Name, Content: p.Content, EnvContent: p.EnvContent, ConfigHash: comp.ProjectHash(p.Content, p.EnvContent), Version: 1, CreatedAt: now, UpdatedAt: now}
	if e := c.d.DB.CreateComposeStack(ctx, rec); e != nil {
		return nil, c.storeError(e, "创建编排失败")
	}
	res, ok, hint := comp.DispatchUp(ctx, c.d.DB, c.d.Ops, c.d.Log, r.AgentID, rec)
	c.d.Log.Info("创建编排", "agent", r.AgentID, "name", rec.Name, "hash", rec.ConfigHash, "下发成功", ok)
	return &api.SaveRes{Stack: comp.StackViewOf(rec, nil), SyncOK: ok, SyncHint: hint, Result: comp.SyncResult(res, ok)}, nil
}
func (c *Controller) Update(ctx context.Context, r *api.UpdateReq) (*api.SaveRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermManage); e != nil {
		return nil, e
	}
	var p api.UpdateInput
	if e := decode(ctx, &p); e != nil {
		return nil, e
	}
	if p.Version <= 0 {
		return nil, controller.Failure("bad_request", "必须提供 version（乐观锁版本号）")
	}
	if e := comp.ValidateContent(p.Content); e != nil {
		return nil, controller.Failure("bad_request", e.Error())
	}
	old, e := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name)
	if e != nil {
		return nil, c.storeError(e, "编排不存在")
	}
	env := old.EnvContent
	if p.EnvContent != nil {
		env = *p.EnvContent
	}
	if e := comp.ValidateEnv(env); e != nil {
		return nil, controller.Failure("bad_request", e.Error())
	}
	rec, ok, e := c.d.DB.UpdateComposeProject(ctx, r.AgentID, r.Name, p.Content, env, comp.ProjectHash(p.Content, env), p.Version, time.Now().UTC())
	if e != nil {
		return nil, c.storeError(e, "编排不存在")
	}
	if !ok {
		return nil, controller.Failure("conflict", "编排内容已被他人修改，请刷新后重新编辑（当前版本与提交的 version 不一致）")
	}
	res, sync, hint := comp.DispatchUp(ctx, c.d.DB, c.d.Ops, c.d.Log, r.AgentID, rec)
	c.d.Log.Info("更新编排", "agent", r.AgentID, "name", r.Name, "version", rec.Version, "下发成功", sync)
	return &api.SaveRes{Stack: comp.StackViewOf(rec, nil), SyncOK: sync, SyncHint: hint, Result: comp.SyncResult(res, sync)}, nil
}
func (c *Controller) Delete(ctx context.Context, r *api.DeleteReq) (*api.DeleteRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermManage); e != nil {
		return nil, e
	}
	rec, e := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name)
	if e != nil {
		return nil, c.storeError(e, "编排不存在")
	}
	remove := false
	switch strings.TrimSpace(strings.ToLower(r.RemoveVolumes)) {
	case "", "0", "false", "f", "no", "n":
	case "1", "true", "t", "yes", "y":
		remove = true
	default:
		return nil, controller.Failure("bad_request", "remove_volumes必须是true或false")
	}
	res, ce := c.d.Ops.ComposeRemove(ctx, r.AgentID, envelope.ComposeOpPayload{Name: r.Name, RemoveVolumes: remove})
	if ce != nil {
		return nil, controller.Failure(ce.Code, "删除未完成，编排记录已保留："+ce.Message)
	}
	if e := c.d.DB.DeleteComposeStack(ctx, r.AgentID, r.Name); e != nil {
		return nil, c.storeError(e, "编排不存在")
	}
	c.d.Log.Info("删除编排", "agent", r.AgentID, "name", r.Name, "下发成功", true)
	return &api.DeleteRes{Name: r.Name, SyncOK: true, SyncHint: "", Result: comp.SyncResult(res, true), ConfigHash: rec.ConfigHash}, nil
}
