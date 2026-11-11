package stackmigration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/service/compose"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/errors/gerror"
)

func uncertain(message string) error {
	return &stackmigrate.ResidualError{Err: gerror.New(message), Residual: stackmigrate.Residual{Uncertain: true}}
}
func connErr(err *agentlink.ConnError) error {
	if err == nil {
		return nil
	}
	return err
}
func (b *Backend) ImportCompose(ctx context.Context, agent, name, config, env, hash string) error {
	if err := compose.ValidateName(name); err != nil {
		return gerror.New("目标编排名无效")
	}
	if err := compose.ValidateContent(config); err != nil {
		return gerror.New("编排内容无效")
	}
	if err := compose.ValidateEnv(env); err != nil {
		return gerror.New("编排环境配置无效")
	}
	if compose.ProjectHash(config, env) != hash {
		return gerror.New("编排哈希不匹配")
	}
	allowed, err := b.CanManage(ctx, agent, name)
	if err != nil || !allowed {
		return gerror.New("没有目标服务器管理权限")
	}
	exists, err := b.StackExists(ctx, agent, name)
	if err != nil {
		return gerror.New("无法确认目标编排状态")
	}
	if exists {
		return gerror.New("目标服务器已有同名编排")
	}
	// Agent performs its own project-lock-protected workspace + engine check.
	out, cerr := b.deps.Ops.ComposeImport(ctx, agent, envelope.ComposeImportPayload{Name: name, Config: config, Env: env, ExpectHash: hash, Overwrite: false})
	if cerr != nil {
		return uncertain("目标配置写入未确认，可能已留下配置；请检查后重试")
	}
	if out.AlreadyExists {
		return gerror.New("目标服务器已有同名编排")
	}
	partial := func(message string) error {
		return &stackmigrate.ResidualError{Err: gerror.New(message), Residual: stackmigrate.Residual{ComposeWritten: true}}
	}
	if out.Name != name || out.ConfigHash != hash {
		return partial("目标返回的配置哈希或名称不匹配")
	}
	// Create-only DB insertion is the authority CAS. Never overwrite a concurrent writer.
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return partial("目标配置已写入，但Hub记录创建失败")
	}
	now := time.Now()
	record := store.ComposeStack{ID: "stack-" + hex.EncodeToString(id[:]), AgentID: agent, Name: name, Content: config, EnvContent: env, ConfigHash: hash, Version: 1, CreatedAt: now, UpdatedAt: now, SyncedAt: now}
	if err := b.deps.DB.CreateComposeStack(ctx, record); err != nil {
		return partial("目标配置已写入，但Hub记录创建冲突或失败，请检查后手工确认")
	}
	return nil
}
func (b *Backend) StartStack(ctx context.Context, agent, name string) error {
	allowed, err := b.CanManage(ctx, agent, name)
	if err != nil || !allowed {
		return gerror.New("没有目标服务器管理权限")
	}
	record, err := b.deps.DB.GetComposeStack(ctx, agent, name)
	if err != nil {
		return gerror.New("目标编排记录不存在")
	}
	plan, cerr := b.deps.Ops.ComposeMigrationPlan(ctx, agent, envelope.ComposeMigrationPlanPayload{Name: name})
	if cerr != nil || plan.Busy || plan.ConfigHash != record.ConfigHash || plan.Config != record.Content || plan.Env != record.EnvContent {
		return gerror.New("目标编排发生变化或正在操作，未启动")
	}
	out, cerr := b.deps.Ops.ComposeUp(ctx, agent, envelope.ComposeSpecPayload{Name: name, Content: record.Content, EnvContent: record.EnvContent, ContentHash: record.ConfigHash})
	if cerr != nil {
		return uncertain("目标启动未确认，可能已部分启动；请检查目标状态")
	}
	if out.Name != name || out.ConfigHash != record.ConfigHash || out.Status != "running" {
		return uncertain("目标编排未全部运行，可能已部分启动")
	}
	return nil
}
