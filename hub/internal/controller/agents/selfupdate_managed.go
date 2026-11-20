package agents

import (
	"context"
	"errors"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
)

func (c *Controller) selfUpdateManagedConfig(ctx context.Context, agentID, project string) (*envelope.SelfUpdateManagedConfig, error) {
	rec, err := c.d.DB.GetComposeStack(ctx, agentID, project)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil // 真正的外部项目仍走宿主机 Compose 配置。
	}
	if err != nil {
		return nil, c.storeError(err, "无法读取本机编排")
	}
	if rec.ConfigHash == "" || rec.SyncedAt.IsZero() || rec.UpdatedAt.After(rec.SyncedAt) {
		return nil, controller.Failure("conflict", "本机编排有尚未同步的配置，已停止自更新；请先核对并同步编排")
	}
	// Hub 记录可能已经被编辑，不能把“自更新”当成部署草稿。
	// 时间戳精度不足以覆盖同秒的修改，必须再核对 Agent 的管理配置哈希。
	list, cerr := agentlink.NewOps(c.d.Registry).ComposeList(ctx, agentID)
	if cerr != nil {
		return nil, controller.ConnectionError(cerr)
	}
	for _, item := range list.Projects {
		if item.Name == project && item.ConfigHash == rec.ConfigHash {
			return &envelope.SelfUpdateManagedConfig{Project: project, Content: rec.Content, EnvContent: rec.EnvContent}, nil
		}
	}
	return nil, controller.Failure("conflict", "本机编排与执行端的配置不一致，已停止自更新；请先核对并同步编排")
}
