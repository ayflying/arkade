package store

// grants.go 是「用户组 → 服务器 → 权限档位」授权关系的仓储层（决策 D-04）。
//
// 两个查询方向，命名上刻意区分（详见各方法注释）：
//   - ListGroupGrants(groupID) —— 这个组被授予了哪些服务器。组管理界面用。
//   - ListUserGrants(userID)   —— 这个用户经过所有组拿到了什么。权限判定用。

import (
	"context"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
	"arkade/hub/internal/user"
)

// grantFromEntity 把 entity 行转成 user.GroupGrant。
func grantFromEntity(e entity.GroupAgents) user.GroupGrant {
	return user.GroupGrant{
		GroupID:    e.GroupId,
		AgentID:    e.AgentId,
		Permission: user.ParsePermission(e.Permission),
	}
}

// SetGroupGrant 设置（新增或覆盖）某个组对某台服务器的权限档位。
func (d *DB) SetGroupGrant(ctx context.Context, groupID, agentID string, perm user.Permission) error {
	c := dao.GroupAgents.Columns()

	// 用「先查后改」而不是 INSERT OR REPLACE：后者会重建整行
	// （触发级联删除副作用），与项目的迁移可移植性要求冲突。
	n, err := d.gdb.Model(dao.GroupAgents.Table()).Ctx(ctx).
		Where(c.GroupId, groupID).
		Where(c.AgentId, agentID).
		Count()
	if err != nil {
		return gerror.Wrap(err, "查询组授权失败")
	}

	if n > 0 {
		if _, err := d.gdb.Model(dao.GroupAgents.Table()).Ctx(ctx).
			Where(c.GroupId, groupID).
			Where(c.AgentId, agentID).
			Data(do.GroupAgents{Permission: perm.String()}).
			Update(); err != nil {
			return gerror.Wrap(err, "更新组授权失败")
		}
		return nil
	}

	if _, err := d.gdb.Model(dao.GroupAgents.Table()).Ctx(ctx).Data(do.GroupAgents{
		GroupId:    groupID,
		AgentId:    agentID,
		Permission: perm.String(),
	}).Insert(); err != nil {
		return normalizeExecError(err, "设置组授权")
	}
	return nil
}

// DeleteGroupGrant 删除某个组对某台服务器的授权。不存在时视为成功（幂等）。
func (d *DB) DeleteGroupGrant(ctx context.Context, groupID, agentID string) error {
	c := dao.GroupAgents.Columns()
	if _, err := d.gdb.Model(dao.GroupAgents.Table()).Ctx(ctx).
		Where(c.GroupId, groupID).
		Where(c.AgentId, agentID).
		Delete(); err != nil {
		return gerror.Wrap(err, "删除组授权失败")
	}
	return nil
}

// ListGroupGrants 按组列出该组被授予的服务器与档位（组管理界面用）。
//
// 注意：这个名字与 ListUserGrants 很容易混。之所以不再提供同名但参数语义不同的
// 方法（曾经踩过坑：两个同名方法一个收 userID、一个收 groupID，编译能过、
// 权限判定却静默失效），这里统一用「名字包含对象」的命名法：
//   - ListGroupGrants(groupID) —— 这个组被授予了什么
//   - ListUserGrants(userID)   —— 这个用户经过所有组拿到了什么
func (d *DB) ListGroupGrants(ctx context.Context, groupID string) ([]user.GroupGrant, error) {
	c := dao.GroupAgents.Columns()
	var ents []entity.GroupAgents
	if err := scanAll(ctx, d.gdb.Model(dao.GroupAgents.Table()).
		Fields(c.GroupId, c.AgentId, c.Permission).
		Where(c.GroupId, groupID).
		Order(c.AgentId+" ASC"), &ents); err != nil {
		return nil, gerror.Wrap(err, "查询组授权失败")
	}

	out := make([]user.GroupGrant, 0, len(ents))
	for _, e := range ents {
		out = append(out, grantFromEntity(e))
	}
	return out, nil
}

// ListUserGrants 查询某用户通过所有用户组获得的服务器授权（权限判定用）。
//
// 返回逐条记录而不是合并后的最大权限：合并逻辑属于权限判定层（authz.go），
// 放在存储层会让 SQL 承担语义判断，不利于测试。
func (d *DB) ListUserGrants(ctx context.Context, userID string) ([]user.GroupGrant, error) {
	// 需要 JOIN（group_agents ⋈ user_groups），保留原生 SQL：
	// 这是多表连接，用 Model 的 LeftJoin 拼字符串可读性不会更好。
	const q = `SELECT ga.group_id, ga.agent_id, ga.permission
	           FROM group_agents ga
	           JOIN user_groups ug ON ug.group_id = ga.group_id
	           WHERE ug.user_id = ?`
	recs, err := d.gdb.GetAll(ctx, q, userID)
	if err != nil {
		return nil, gerror.Wrap(err, "查询用户组授权失败")
	}

	out := make([]user.GroupGrant, 0, len(recs))
	for _, r := range recs {
		out = append(out, user.GroupGrant{
			GroupID:    r["group_id"].String(),
			AgentID:    r["agent_id"].String(),
			Permission: user.ParsePermission(r["permission"].String()),
		})
	}
	return out, nil
}

// SetGroupGrants 在一个事务里把某个组的授权整体替换成给定集合。
//
// 与 SetGroupMembers 同样的理由：整体替换（先清后写）比差分更新更能表达
// 「这是该组当前的完整授权」，也避免两个人同时编辑时互相把对方的改动冲掉。
func (d *DB) SetGroupGrants(ctx context.Context, groupID string, grants []user.GroupGrant) error {
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		exist, err := tx.Model(dao.Groups.Table()).Ctx(ctx).
			Where(dao.Groups.Columns().Id, groupID).
			Count()
		if err != nil {
			return gerror.Wrap(err, "查询用户组失败")
		}
		if exist == 0 {
			return ErrNotFound
		}

		if _, err := tx.Model(dao.GroupAgents.Table()).Ctx(ctx).
			Where(dao.GroupAgents.Columns().GroupId, groupID).
			Delete(); err != nil {
			return gerror.Wrap(err, "清空组授权失败")
		}

		// 同组同一个 agent 只能有一条授权：入参里重复的 agent_id 取最后一次，
		// 顺序即调用方意图（前端通常按列表顺序渲染，用户最后改的那条最符合预期）。
		merged := make(map[string]user.Permission, len(grants))
		order := make([]string, 0, len(grants))
		for _, gr := range grants {
			if _, ok := merged[gr.AgentID]; !ok {
				order = append(order, gr.AgentID)
			}
			merged[gr.AgentID] = gr.Permission
		}
		if len(order) == 0 {
			return nil
		}

		for _, agentID := range order {
			if _, err := tx.Model(dao.GroupAgents.Table()).Ctx(ctx).Data(do.GroupAgents{
				GroupId:    groupID,
				AgentId:    agentID,
				Permission: merged[agentID].String(),
			}).Insert(); err != nil {
				return normalizeExecError(err, "写入组授权")
			}
		}
		return nil
	})
}

// CountGroupGrants 统计某组的授权数量（用于删除前的提示与列表展示）。
func (d *DB) CountGroupGrants(ctx context.Context, groupID string) (int, error) {
	n, err := d.gdb.Model(dao.GroupAgents.Table()).Ctx(ctx).
		Where(dao.GroupAgents.Columns().GroupId, groupID).
		Count()
	if err != nil {
		return 0, gerror.Wrap(err, "统计组授权失败")
	}
	return n, nil
}
