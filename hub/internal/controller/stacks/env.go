package stacks

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/controller"
	comp "arkade/hub/internal/service/compose"
	"arkade/hub/internal/user"
	"context"
	"time"
)

func (c *Controller) PatchEnv(ctx context.Context, r *api.PatchEnvReq) (*api.PatchEnvRes, error) {
	if err := c.check(ctx, r.AgentID, r.Name, user.PermManage); err != nil {
		return nil, err
	}
	var p api.PatchEnvInput
	if err := decode(ctx, &p); err != nil {
		return nil, controller.Failure("bad_request", "环境变量更新请求格式无效")
	}
	if p.Version <= 0 || len(p.Updates) == 0 || len(p.Updates) > 128 {
		return nil, controller.Failure("bad_request", "必须提供当前版本及 1 至 128 个待更新的变量")
	}
	old, err := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name)
	if err != nil {
		return nil, c.storeError(err, "编排不存在")
	}
	conflict := controller.Failure("conflict", "编排版本已变化，请重新读取后再保存")
	if old.Version != p.Version {
		return nil, conflict
	}
	env, keys, err := comp.MergeEnv(old.EnvContent, p.Updates)
	if err != nil {
		return nil, controller.Failure("bad_request", err.Error())
	}
	if err := comp.ValidateEnv(env); err != nil {
		return nil, controller.Failure("bad_request", err.Error())
	}
	if len(keys) == 0 {
		return &api.PatchEnvRes{Version: old.Version, ChangedKeys: []string{}}, nil
	}
	// 只保存 Hub 记录；部署时再通过现有流程下发完整配置。
	rec, ok, err := c.d.DB.UpdateComposeProject(ctx, r.AgentID, r.Name, old.Content, env, comp.ProjectHash(old.Content, env), p.Version, time.Now().UTC())
	if err != nil {
		return nil, controller.Failure("internal", "保存环境变量失败")
	}
	if !ok {
		return nil, conflict
	}
	return &api.PatchEnvRes{Version: rec.Version, ChangedKeys: keys}, nil
}
