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
	// 明文单独写：写失败只影响"以后还能不能再看密钥"，
	// 不该让已经建好的记录和已返回的密钥失效（重试创建只会撞重名）。
	if err := c.d.DB.SetAgentTokenPlain(ctx, id, plain); err != nil {
		c.d.Log.Warn("保存接入密钥明文失败，该服务器将无法再次查看密钥", "agent_id", id, "err", err)
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
	// 重置一定发生在"服务器还要重新接入"的语境里，因此新密钥同样保留可回读副本。
	if err := c.d.DB.SetAgentTokenPlain(ctx, req.AgentID, plain); err != nil {
		c.d.Log.Warn("保存接入密钥明文失败，该服务器将无法再次查看密钥", "agent_id", req.AgentID, "err", err)
	}
	secretHeaders(ctx)
	return &api.ResetTokenRes{Agent: view, Enrollment: api.EnrollmentView{Token: plain, AgentID: req.AgentID}}, nil
}

// GetEnrollment 查看「尚未接入」服务器的密钥与编排信息。
//
// 存在这件事的理由：用户在「添加服务器」后常常过一阵才去那台机器部署，
// 期间要反复核对密钥和编排示例。只给一次就得靠"重置"才能再看，
// 而重置会让已经开始连接的 Agent 掉线。
//
// 只对 pending（从未接入）开放，其余一律 409：接入之后列表不再提供查看入口，
// 那时也确有"重置"这条正当路径。
func (c *Controller) GetEnrollment(ctx context.Context, req *api.GetEnrollmentReq) (*api.GetEnrollmentRes, error) {
	if err := c.requireAdmin(ctx); err != nil {
		return nil, err
	}
	rec, err := c.d.DB.GetAgent(ctx, req.AgentID)
	if err != nil {
		return nil, c.storeError(err, "服务器不存在")
	}
	if rec.Status != store.StatusPending {
		return nil, &controller.Error{Status: 409, Code: "conflict", Message: "该服务器已接入，接入密钥只在添加时展示一次；需要重新获取请重置密钥"}
	}
	plain, err := c.d.DB.AgentTokenPlain(ctx, req.AgentID)
	if err != nil {
		if errors.Is(err, store.ErrAgentTokenNotFound) {
			return nil, &controller.Error{Status: 409, Code: "conflict", Message: "该服务器的接入信息已不可查看，请重置密钥"}
		}
		return nil, c.storeError(err, "服务器不存在")
	}
	if plain == "" {
		return nil, &controller.Error{Status: 409, Code: "conflict", Message: "该服务器的接入信息已不可查看，请重置密钥"}
	}
	view, err := c.viewByID(ctx, req.AgentID)
	if err != nil {
		return nil, err
	}
	secretHeaders(ctx)
	return &api.GetEnrollmentRes{Agent: view, Enrollment: api.EnrollmentView{Token: plain, AgentID: req.AgentID}}, nil
}

// The only transport exception: one-shot credentials must never be cached.
func secretHeaders(ctx context.Context) {
	if r := ghttp.RequestFromCtx(ctx); r != nil {
		r.Response.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		r.Response.Header().Set("Pragma", "no-cache")
		r.Response.Header().Set("Expires", "0")
	}
}
