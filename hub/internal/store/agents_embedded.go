package store

import (
	"context"
	"time"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"
)

// EnsureEmbeddedLocalAgent 预建无需网络凭据的内置本机身份。
// 既有身份必须是无网络凭据的内置记录；不接管远程服务器或转换旧凭据。
// preprovisioned 标记防止共享 token 认领，Registry 保留身份防止所有远程冒充。
// 未调用本方法（例如 local.enabled=false）不会删除或修改既有身份。
func (d *DB) EnsureEmbeddedLocalAgent(ctx context.Context) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		// 跨连接池串行化固定身份的检查与新建，避免首次并发启动互相冲突。
		if _, err := tx.Exec("SELECT pg_advisory_xact_lock(734281905)"); err != nil {
			return gerror.Wrap(err, "锁定内置本机身份失败")
		}
		ac := dao.Agents.Columns()
		tokens, err := tx.Model(dao.AgentTokens.Table()).Ctx(ctx).Where(dao.AgentTokens.Columns().AgentId, LocalAgentID).Count()
		if err != nil {
			return gerror.Wrap(err, "检查内置本机身份冲突失败")
		}
		if tokens != 0 {
			return gerror.New("内置本机固定 ID 已关联远程凭据，拒绝接管")
		}
		rec, err := tx.Model(dao.Agents.Table()).Ctx(ctx).Where(ac.Id, LocalAgentID).One()
		if err != nil {
			return gerror.Wrap(err, "查询内置本机身份失败")
		}
		if !rec.IsEmpty() {
			if rec[ac.Preprovisioned].Int() != 1 {
				return gerror.New("内置本机固定 ID 已被非预建记录占用，拒绝接管")
			}
		} else {
			dup, err := agentNameExistsTx(ctx, tx, LocalAgentName, "")
			if err != nil {
				return err
			}
			if dup {
				return ErrAgentNameTaken
			}
			now := time.Now().Unix()
			_, err = tx.Model(dao.Agents.Table()).Ctx(ctx).Data(do.Agents{Id: LocalAgentID, Name: LocalAgentName, DisplayName: LocalAgentName, Status: "active", Version: "", Runtime: "", RuntimeVersion: "", LastSeen: 0, Labels: "{}", CreatedAt: now, UpdatedAt: now, Disabled: 0, Preprovisioned: 1}).Insert()
			if err != nil {
				return normalizeExecError(err, "创建内置本机身份")
			}
		}
		return nil
	})
}

// UpdateEmbeddedLocalAgentRuntime 仅更新内置本机运行快照。不可直接复用
// UpsertAgent：它会覆盖 Name，抹掉本机身份的管理员命名。此方法不新建身份，
// 不改变 disabled、labels、备注、显示名、地址、preprovisioned 或分组归属。
func (d *DB) UpdateEmbeddedLocalAgentRuntime(ctx context.Context, rec AgentRecord) error {
	if rec.ID != "" && rec.ID != LocalAgentID {
		return gerror.New("内置本机运行状态只能更新固定 ID")
	}
	c := dao.Agents.Columns()
	result, err := d.gdb.Model(dao.Agents.Table()).Ctx(ctx).Where(c.Id, LocalAgentID).Where(c.Preprovisioned, 1).Data(do.Agents{Status: rec.Status, Version: rec.Version, Runtime: rec.Runtime, RuntimeVersion: rec.RuntimeVersion, LastSeen: rec.LastSeen, UpdatedAt: rec.UpdatedAt}).Update()
	if err != nil {
		return gerror.Wrap(err, "更新内置本机运行状态失败")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return gerror.Wrap(err, "确认内置本机运行状态失败")
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
