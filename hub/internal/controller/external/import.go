package external

import (
	"context"
	"errors"
	"time"

	api "arkade/hub/api/external/v1"
	"arkade/hub/internal/controller"
	compose "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

func validateImport(input api.ImportInput) error {
	for _, err := range []error{compose.ValidateName(input.Name), compose.ValidateContent(input.Content), compose.ValidateEnv(input.EnvContent)} {
		if err != nil {
			return controller.Failure("bad_request", err.Error())
		}
	}
	return nil
}
func (c *Controller) ImportPreview(ctx context.Context, req *api.ImportPreviewReq) (*api.ImportPreviewRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	var input api.ImportInput
	if err := decode(ctx, &input); err != nil {
		return nil, err
	}
	if err := validateImport(input); err != nil {
		return nil, err
	}
	result, cerr := c.deps.Ops.ComposePreview(ctx, req.AgentID, envelope.ComposePreviewPayload{Name: input.Name, Content: input.Content, EnvContent: input.EnvContent})
	if cerr != nil {
		return nil, controller.Failure(cerr.Code, "导入预检失败："+cerr.Message)
	}
	hash := compose.ProjectHash(input.Content, input.EnvContent)
	if existing, err := c.deps.DB.GetComposeStack(ctx, req.AgentID, input.Name); err == nil && (!existing.SyncedAt.IsZero() || existing.ConfigHash != hash) {
		result.CanImport = false
		result.Blockers = append(result.Blockers, "Hub已存在该项目或配置与未同步记录不一致，请使用项目编辑而非重复导入")
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, c.storeError(err, "检查导入项目失败")
	}
	secret(ctx)
	return &api.ImportPreviewRes{Name: input.Name, ConfigHash: hash, CanImport: result.CanImport && result.ConfigHash == hash && len(result.Blockers) == 0, Blockers: result.Blockers, Warnings: result.Warnings}, nil
}

func (c *Controller) Import(ctx context.Context, req *api.ImportReq) (*api.ImportRes, error) {
	if err := c.access.Require(ctx, req.AgentID, user.PermManage); err != nil {
		return nil, err
	}
	var input api.ImportInput
	if err := decode(ctx, &input); err != nil {
		return nil, err
	}
	if err := validateImport(input); err != nil {
		return nil, err
	}
	hash := compose.ProjectHash(input.Content, input.EnvContent)
	if !input.Confirmation || input.ExpectedContentHash != hash {
		return nil, controller.Failure("bad_request", "请先预检当前配置并明确确认导入；配置已变化时必须重新预检")
	}
	preview, cerr := c.deps.Ops.ComposePreview(ctx, req.AgentID, envelope.ComposePreviewPayload{Name: input.Name, Content: input.Content, EnvContent: input.EnvContent})
	if cerr != nil {
		return nil, controller.Failure(cerr.Code, "导入预检失败："+cerr.Message)
	}
	if !preview.CanImport || preview.ConfigHash != hash || len(preview.Blockers) > 0 {
		return nil, controller.Failure("conflict", "当前项目不满足安全导入条件，请重新预检并解决阻塞项")
	}
	rec := newRecord(req.AgentID, input.Name, input.Content, input.EnvContent)
	if err := c.deps.DB.CreateComposeStack(ctx, rec); err != nil {
		if !errors.Is(err, store.ErrConflict) {
			return nil, c.storeError(err, "导入保存失败")
		}
		existing, getErr := c.deps.DB.GetComposeStack(ctx, req.AgentID, input.Name)
		if getErr != nil || !existing.SyncedAt.IsZero() || existing.ConfigHash != hash {
			return nil, c.storeError(err, "项目已存在，不能重复导入")
		}
		rec = existing
	}
	result, cerr := c.deps.Ops.ComposeAdopt(ctx, req.AgentID, envelope.ComposeAdoptPayload{Name: input.Name, Content: input.Content, EnvContent: input.EnvContent, ExpectedContentHash: hash, Confirmation: true})
	ok := cerr == nil && result.Adopted && result.ConfigHash == hash
	hint := "仅导入配置，未启动、停止或重建任何容器"
	if !ok {
		hint = "导入配置已保存，但Agent未确认正确接管；请重新执行导入，不要启动或重建"
		if cerr != nil {
			hint = "导入配置已保存，但Agent接管未完成；请勿执行启动或重建：" + cerr.Message
		}
	} else if err := c.deps.DB.MarkComposeStackSynced(ctx, req.AgentID, input.Name); err != nil {
		c.deps.Log.Error("记录导入同步时间失败", "agent", req.AgentID, "name", input.Name)
	}
	return &api.ImportRes{Stack: compose.StackViewOf(rec, nil), SyncOK: ok, SyncHint: hint, Adopted: ok && result.Adopted}, nil
}

func newRecord(agent, name, content, env string) store.ComposeStack {
	now := time.Now().UTC()
	return store.ComposeStack{ID: newID(), AgentID: agent, Name: name, Content: content, EnvContent: env, ConfigHash: compose.ProjectHash(content, env), Version: 1, CreatedAt: now, UpdatedAt: now}
}
