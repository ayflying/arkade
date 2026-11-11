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
	var plain string
	err := c.revoke(ctx, func() error {
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

// The only transport exception: one-shot credentials must never be cached.
func secretHeaders(ctx context.Context) {
	if r := ghttp.RequestFromCtx(ctx); r != nil {
		r.Response.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		r.Response.Header().Set("Pragma", "no-cache")
		r.Response.Header().Set("Expires", "0")
	}
}
