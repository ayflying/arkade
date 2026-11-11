package external

import (
	"context"
	"errors"

	api "arkade/hub/api/external/v1"
	"arkade/hub/internal/controller"
	compose "arkade/hub/internal/service/compose"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

func (c *Controller) ApplyPreview(ctx context.Context, req *api.ApplyPreviewReq) (*api.ApplyPreviewRes, error) {
	if _, err := c.requireExternal(ctx, req.AgentID, req.Name, user.PermManage); err != nil {
		return nil, err
	}
	var input api.ApplyPreviewInput
	if err := decode(ctx, &input); err != nil {
		return nil, err
	}
	if err := compose.ValidateContent(input.Content); err != nil {
		return nil, controller.Failure("bad_request", err.Error())
	}
	preview, cerr := c.deps.Ops.ComposePreview(ctx, req.AgentID, envelope.ComposePreviewPayload{Name: req.Name, Content: input.Content})
	if cerr != nil {
		return nil, controller.ConnectionError(cerr)
	}
	secret(ctx)
	return &api.ApplyPreviewRes{CanImport: preview.CanImport, ConfigHash: preview.ConfigHash, Blockers: preview.Blockers, Warnings: preview.Warnings}, nil
}

func (c *Controller) Apply(ctx context.Context, req *api.ApplyReq) (*api.ApplyRes, error) {
	if _, err := c.requireExternal(ctx, req.AgentID, req.Name, user.PermManage); err != nil {
		return nil, err
	}
	var input api.ImportInput
	if err := decode(ctx, &input); err != nil {
		return nil, err
	}
	input.Name = req.Name
	if err := validateImport(input); err != nil {
		return nil, err
	}
	hash := compose.ProjectHash(input.Content, input.EnvContent)
	if !input.Confirmation || input.ExpectedContentHash != hash {
		return nil, controller.Failure("bad_request", "请先预检当前配置并明确确认登记；登记不修改正在运行的容器")
	}
	preview, cerr := c.deps.Ops.ComposePreview(ctx, req.AgentID, envelope.ComposePreviewPayload{Name: req.Name, Content: input.Content, EnvContent: input.EnvContent})
	if cerr != nil {
		return nil, controller.ConnectionError(cerr)
	}
	if !preview.CanImport || preview.ConfigHash != hash || len(preview.Blockers) > 0 {
		return nil, controller.Failure("conflict", "当前配置未通过安全登记预检，草稿仍保留，未改变运行中的项目")
	}
	rec := newRecord(req.AgentID, req.Name, input.Content, input.EnvContent)
	if err := c.deps.DB.CreateComposeStack(ctx, rec); err != nil {
		existing, getErr := c.deps.DB.GetComposeStack(ctx, req.AgentID, req.Name)
		if !errors.Is(err, store.ErrConflict) || getErr != nil || !existing.SyncedAt.IsZero() || existing.ConfigHash != hash {
			return nil, c.storeError(err, "同名编排已存在，请从已纳管项目继续管理")
		}
		rec = existing
	}
	result, cerr := c.deps.Ops.ComposeAdopt(ctx, req.AgentID, envelope.ComposeAdoptPayload{Name: req.Name, Content: input.Content, EnvContent: input.EnvContent, ExpectedContentHash: hash, Confirmation: true})
	ok := cerr == nil && result.Adopted && result.ConfigHash == hash
	hint := "已登记到Arkade，未修改宿主原文件，未启动或重建容器"
	if ok {
		if err := c.deps.DB.MarkComposeStackSynced(ctx, req.AgentID, req.Name); err != nil {
			ok = false
			hint = "登记完成但同步状态保存失败，请刷新确认"
		}
	} else {
		hint = "Hub已保存配置，但Agent尚未完成登记；不会启动或重建容器"
	}
	secret(ctx)
	return &api.ApplyRes{Stack: compose.StackViewOf(rec, nil), SyncOK: ok, SyncHint: hint, Adopted: ok}, nil
}
