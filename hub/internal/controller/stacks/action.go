package stacks

import (
	api "arkade/hub/api/stacks/v1"
	"arkade/hub/internal/controller"
	comp "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
	"context"
	"fmt"
)

func (c *Controller) Action(ctx context.Context, r *api.ActionReq) (*api.ActionRes, error) {
	if e := c.check(ctx, r.AgentID, r.Name, user.PermOperate); e != nil {
		return nil, e
	}
	var p api.ActionInput
	if e := decode(ctx, &p); e != nil {
		return nil, e
	}
	rec, e := c.d.DB.GetComposeStack(ctx, r.AgentID, r.Name)
	if e != nil {
		return nil, c.storeError(e, "编排不存在")
	}
	payload := envelope.ComposeOpPayload{Name: r.Name, Services: p.Services, RemoveVolumes: p.RemoveVolumes, Force: p.Action == "rebuild"}
	var res envelope.ComposeOpResult
	switch p.Action {
	case "up":
		result, ce := c.d.Ops.ComposeUp(ctx, r.AgentID, envelope.ComposeSpecPayload{Name: rec.Name, Content: rec.Content, EnvContent: rec.EnvContent, ContentHash: rec.ConfigHash})
		if ce != nil {
			return nil, controller.ConnectionError(ce)
		}
		res = result
	case "down":
		result, ce := c.d.Ops.ComposeDown(ctx, r.AgentID, payload)
		if ce != nil {
			return nil, controller.ConnectionError(ce)
		}
		res = result
	case "stop", "restart", "pull", "rebuild":
		result, ce := comp.SimpleAction(ctx, c.d.Ops, r.AgentID, p.Action, payload)
		if ce != nil {
			return nil, controller.ConnectionError(ce)
		}
		res = result
	case "remove":
		if e := c.access.Require(ctx, r.AgentID, user.PermManage); e != nil {
			return nil, e
		}
		result, ce := c.d.Ops.ComposeRemove(ctx, r.AgentID, payload)
		if ce != nil {
			return nil, controller.ConnectionError(ce)
		}
		res = result
		if e := c.d.DB.DeleteComposeStack(ctx, r.AgentID, r.Name); e != nil && e != store.ErrNotFound {
			c.d.Log.Warn("删除编排记录失败", "agent", r.AgentID, "name", r.Name, "err", e)
		}
	case "backup":
		result, ce := c.d.Ops.ComposeBackup(ctx, r.AgentID, envelope.ComposeBackupPayload{Name: r.Name, IncludeVolumes: p.IncludeVolumes})
		if ce != nil {
			return nil, controller.ConnectionError(ce)
		}
		return &api.ActionRes{Backup: &api.BackupResult{Name: result.Name, Path: result.Path, SizeBytes: result.SizeBytes, Files: result.Files, CreatedAt: result.CreatedAt}}, nil
	default:
		return nil, controller.Failure("bad_request", fmt.Sprintf("不支持的 action %q（可用：up/down/stop/restart/pull/rebuild/remove/backup）", p.Action))
	}
	v := comp.OpResultView(r.AgentID, res)
	return &api.ActionRes{Operation: &v}, nil
}
