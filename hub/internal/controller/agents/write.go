package agents

import (
	api "arkade/hub/api/agents/v1"
	service "arkade/hub/internal/service/agents"
	"arkade/hub/internal/store"
	"context"
	"strings"
	"time"
)

func (c *Controller) Patch(ctx context.Context, req *api.PatchReq) (*api.PatchRes, error) {
	if err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	var err error
	req, err = patchBody(ctx, req)
	if err != nil {
		return nil, err
	}
	fields := store.AgentAdminFields{}
	if req.DirectAddress != nil {
		v, err := service.NormalizeDirectAddress(*req.DirectAddress)
		if err != nil {
			return nil, badRequest(err.Error())
		}
		fields.DirectAddress = &v
	}
	if req.DisplayName != nil {
		v := strings.TrimSpace(*req.DisplayName)
		if len([]rune(v)) > 64 {
			return nil, badRequest("显示名不能超过 64 个字符")
		}
		fields.DisplayName = &v
	}
	if req.Note != nil {
		v := strings.TrimSpace(*req.Note)
		if len([]rune(v)) > 500 {
			return nil, badRequest("备注不能超过 500 个字符")
		}
		fields.Note = &v
	}
	if req.Labels != nil {
		if msg := service.ValidateLabels(*req.Labels); msg != "" {
			return nil, badRequest(msg)
		}
		fields.Labels = req.Labels
	}
	rec, err := c.d.DB.UpdateAgentAdmin(ctx, req.AgentID, fields, time.Now().Unix())
	if err != nil {
		return nil, c.storeError(err, "服务器不存在")
	}
	return &api.PatchRes{Agent: c.mutationView(rec)}, nil
}

const DeleteAgentCloseReason = "该服务器记录已被管理员移除，连接已断开；如需继续纳管请重新接入"

func (c *Controller) Delete(ctx context.Context, req *api.DeleteReq) (*api.DeleteRes, error) {
	if err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	before, err := c.d.DB.CountComposeStacks(ctx, req.AgentID)
	if err != nil {
		return nil, c.storeError(err, "统计编排数量失败")
	}
	err = c.revoke(ctx, func() error {
		if err := c.d.DB.DeleteAgent(ctx, req.AgentID); err != nil {
			return err
		}
		if err := c.d.DB.DeleteAgentToken(ctx, req.AgentID); err != nil {
			c.d.Log.Error("删除服务器后清理接入凭据失败，该凭据可能仍然可用", "agent_id", req.AgentID, "err", err)
		}
		c.d.Registry.Close(req.AgentID, DeleteAgentCloseReason)
		return nil
	})
	if err != nil {
		return nil, c.revocationError(err, "服务器不存在")
	}
	return &api.DeleteRes{Deleted: true, AgentID: req.AgentID, OrphanStacks: before, Message: "服务器记录已删除，机器上的容器未受影响；原有的编排配置仍保留在服务端，重新接入该服务器后可继续使用"}, nil
}
func (c *Controller) Disable(ctx context.Context, req *api.DisableReq) (*api.DisableRes, error) {
	view, err := c.setDisabled(ctx, req.AgentID, true)
	if err != nil {
		return nil, err
	}
	return &api.DisableRes{Agent: view}, nil
}
func (c *Controller) Enable(ctx context.Context, req *api.EnableReq) (*api.EnableRes, error) {
	view, err := c.setDisabled(ctx, req.AgentID, false)
	if err != nil {
		return nil, err
	}
	return &api.EnableRes{Agent: view}, nil
}
func (c *Controller) setDisabled(ctx context.Context, id string, disabled bool) (api.AgentView, error) {
	if err := c.requireAdmin(ctx); err != nil {
		return api.AgentView{}, err
	}
	err := c.revoke(ctx, func() error {
		if err := c.d.DB.SetAgentDisabled(ctx, id, disabled, time.Now().Unix()); err != nil {
			return err
		}
		if disabled {
			c.d.Registry.Close(id, "该服务器已被管理员禁用，连接已断开")
		}
		return nil
	})
	if err != nil {
		return api.AgentView{}, c.revocationError(err, "服务器不存在")
	}
	rec, err := c.d.DB.GetAgent(ctx, id)
	if err != nil {
		return api.AgentView{}, c.storeError(err, "服务器不存在")
	}
	return c.mutationView(rec), nil
}
