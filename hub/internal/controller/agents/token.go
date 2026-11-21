package agents

import (
	api "arkade/hub/api/agents/v1"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	service "arkade/hub/internal/service/agents"
	"arkade/hub/internal/store"
	"context"
	"errors"
	"github.com/gogf/gf/v2/net/ghttp"
	"strings"
	"time"
)

func (c *Controller) Create(ctx context.Context, req *api.CreateReq) (*api.CreateRes, error) {
	if err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	var err error
	req, err = createBody(ctx, req)
	if err != nil {
		return nil, err
	}
	name, err := service.ValidateCreate(req.Name, req.Note, req.Labels)
	if err != nil {
		return nil, badRequest(err.Error())
	}
	plain, hash, err := agentlink.GenerateEnrollmentTokenAvoiding(c.d.SharedToken)
	if err != nil {
		c.d.Log.Error("生成接入密钥失败", "err", err)
		return nil, internal("生成接入密钥失败，请重试", err)
	}
	id, err := store.NewAgentID()
	if err != nil {
		c.d.Log.Error("生成服务器 ID 失败", "err", err)
		return nil, internal("创建服务器失败，请重试", err)
	}
	now := time.Now().UTC()
	rec := store.AgentRecord{ID: id, Name: name, DisplayName: name, Note: strings.TrimSpace(req.Note), Labels: req.Labels, CreatedAt: now.Unix(), UpdatedAt: now.Unix()}
	if err := c.d.DB.CreatePreprovisionedAgent(ctx, rec, hash, now); err != nil {
		if errors.Is(err, store.ErrAgentNameTaken) {
			return nil, &controller.Error{Status: 409, Code: "conflict", Message: "已存在同名的服务器，请换一个名称"}
		}
		return nil, c.storeError(err, "创建服务器失败")
	}
	view, err := c.viewByID(ctx, id)
	if err != nil {
		return nil, err
	}
	secretHeaders(ctx)
	controller.Status(ctx, 201)
	return &api.CreateRes{Agent: view, Enrollment: api.EnrollmentView{Token: plain, AgentID: id}}, nil
}

func (c *Controller) ResetToken(ctx context.Context, req *api.ResetTokenReq) (*api.ResetTokenRes, error) {
	if err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := c.rejectReservedLocal(req.AgentID); err != nil {
		return nil, err
	}
	var plain string
	err := c.revoke(ctx, func() error {
		if err := c.rejectReservedLocal(req.AgentID); err != nil {
			return err
		}
		p, h, err := agentlink.GenerateEnrollmentTokenAvoiding(c.d.SharedToken)
		if err != nil {
			return err
		}
		plain = p
		if err := c.d.DB.ResetAgentTokenHash(ctx, req.AgentID, h, time.Now().UTC()); err != nil {
			return err
		}
		c.d.Registry.Close(req.AgentID, "该服务器的接入密钥已被重置，请使用新密钥重新连接")
		return nil
	})
	if err != nil {
		return nil, c.revocationError(err, "重置接入密钥失败")
	}
	view, err := c.viewByID(ctx, req.AgentID)
	if err != nil {
		return nil, err
	}
	secretHeaders(ctx)
	return &api.ResetTokenRes{Agent: view, Enrollment: api.EnrollmentView{Token: plain, AgentID: req.AgentID}}, nil
}

// RenewEnrollment 为「尚未接入」的服务器换发一把新密钥，并返回接入信息。
//
// 为什么是"换发"而不是"回看历史"：库里不保存任何可解密的密钥副本，
// 所以想看就得重新生成——这样即使数据库被拖走，也拿不到能直接用的密钥。
//
// 怎么避免把已经接上的服务器踢下线：这里绝不调用 Registry.Close，
// 只在"从未接入"（pending）时换发。旧密钥的摘要被覆盖，因此旧密钥立即作废；
// 由于这台机器本来就还没连上来，不会有任何在跑的连接被打断。
//
// 只对 pending 开放，其余一律 409：已接入的服务器走"重置密钥"那条明确的危险路径，
// 用户在那条路径上会看到"会断开连接"的二次确认。
func (c *Controller) RenewEnrollment(ctx context.Context, req *api.RenewEnrollmentReq) (*api.RenewEnrollmentRes, error) {
	if err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	if err := c.rejectReservedLocal(req.AgentID); err != nil {
		return nil, err
	}
	// 先做一次快速判断只为给出更早的 404/409 提示；
	// 真正的原子校验在 RenewPendingAgentToken 的事务里（含行锁）。
	rec, err := c.d.DB.GetAgent(ctx, req.AgentID)
	if err != nil {
		return nil, c.storeError(err, "服务器不存在")
	}
	if rec.Status != store.StatusPending {
		return nil, &controller.Error{Status: 409, Code: "conflict", Message: "该服务器已接入，接入密钥只在添加时展示一次；需要重新获取请重置密钥"}
	}
	plain, hash, err := agentlink.GenerateEnrollmentTokenAvoiding(c.d.SharedToken)
	if err != nil {
		c.d.Log.Error("生成接入密钥失败", "err", err)
		return nil, internal("生成接入密钥失败，请重试", err)
	}
	// 状态校验与摘要替换在同一个事务里完成：见 store.RenewPendingAgentToken 的说明。
	if _, err := c.d.DB.RenewPendingAgentToken(ctx, req.AgentID, hash, time.Now().UTC()); err != nil {
		if errors.Is(err, store.ErrNotPending) {
			return nil, &controller.Error{Status: 409, Code: "conflict", Message: "该服务器已接入，接入密钥只在添加时展示一次；需要重新获取请重置密钥"}
		}
		return nil, c.storeError(err, "换发接入密钥失败")
	}
	view, err := c.viewByID(ctx, req.AgentID)
	if err != nil {
		return nil, err
	}
	secretHeaders(ctx)
	return &api.RenewEnrollmentRes{Agent: view, Enrollment: api.EnrollmentView{Token: plain, AgentID: req.AgentID}}, nil
}

// The only transport exception: one-shot credentials must never be cached.
func secretHeaders(ctx context.Context) {
	if r := ghttp.RequestFromCtx(ctx); r != nil {
		r.Response.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		r.Response.Header().Set("Pragma", "no-cache")
		r.Response.Header().Set("Expires", "0")
	}
}
