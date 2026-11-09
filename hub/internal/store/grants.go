package store

// grants.go 是「用户组 → 服务器 → 权限档位」授权关系的仓储层（决策 D-04）。
//
// 两个查询方向，命名上刻意区分（详见各方法注释）：
//   - ListGroupGrants(groupID) —— 这个组被授予了哪些服务器。组管理界面用。
//   - ListUserGrants(userID)   —— 这个用户经过所有组拿到了什么。权限判定用。

import (
	"context"
	"fmt"

	"arkade/hub/internal/user"
)

// SetGroupGrant 设置（新增或覆盖）某个组对某台服务器的权限档位。
func (d *DB) SetGroupGrant(ctx context.Context, groupID, agentID string, perm user.Permission) error {
	// 用「先查后改」而不是 INSERT OR REPLACE：后者是 SQLite 专有语法，
	// 且会重建整行（触发级联删除副作用），与项目的迁移可移植性要求冲突。
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM group_agents WHERE group_id = ? AND agent_id = ?`,
		groupID, agentID).Scan(&n); err != nil {
		return fmt.Errorf("查询组授权失败: %w", err)
	}
	if n > 0 {
		if _, err := d.db.ExecContext(ctx,
			`UPDATE group_agents SET permission = ? WHERE group_id = ? AND agent_id = ?`,
			perm.String(), groupID, agentID); err != nil {
			return fmt.Errorf("更新组授权失败: %w", err)
		}
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`INSERT INTO group_agents(group_id, agent_id, permission) VALUES(?, ?, ?)`,
		groupID, agentID, perm.String()); err != nil {
		return normalizeExecError(err, "设置组授权")
	}
	return nil
}

// DeleteGroupGrant 删除某个组对某台服务器的授权。不存在时视为成功（幂等）。
func (d *DB) DeleteGroupGrant(ctx context.Context, groupID, agentID string) error {
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM group_agents WHERE group_id = ? AND agent_id = ?`, groupID, agentID); err != nil {
		return fmt.Errorf("删除组授权失败: %w", err)
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
	const q = `SELECT group_id, agent_id, permission
	           FROM group_agents WHERE group_id = ? ORDER BY agent_id ASC`
	rows, err := d.db.QueryContext(ctx, q, groupID)
	if err != nil {
		return nil, fmt.Errorf("查询组授权失败: %w", err)
	}
	defer rows.Close()

	out := make([]user.GroupGrant, 0, 16)
	for rows.Next() {
		var g user.GroupGrant
		var perm string
		if err := rows.Scan(&g.GroupID, &g.AgentID, &perm); err != nil {
			return nil, fmt.Errorf("扫描组授权失败: %w", err)
		}
		g.Permission = user.ParsePermission(perm)
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListUserGrants 查询某用户通过所有用户组获得的服务器授权（权限判定用）。
//
// 返回逐条记录而不是合并后的最大权限：合并逻辑属于权限判定层（authz.go），
// 放在存储层会让 SQL 承担语义判断，不利于测试。
func (d *DB) ListUserGrants(ctx context.Context, userID string) ([]user.GroupGrant, error) {
	const q = `SELECT ga.group_id, ga.agent_id, ga.permission
	           FROM group_agents ga
	           JOIN user_groups ug ON ug.group_id = ga.group_id
	           WHERE ug.user_id = ?`
	rows, err := d.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("查询用户组授权失败: %w", err)
	}
	defer rows.Close()

	out := make([]user.GroupGrant, 0, 16)
	for rows.Next() {
		var g user.GroupGrant
		var perm string
		if err := rows.Scan(&g.GroupID, &g.AgentID, &perm); err != nil {
			return nil, fmt.Errorf("扫描用户组授权失败: %w", err)
		}
		g.Permission = user.ParsePermission(perm)
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetGroupGrants 在一个事务里把某个组的授权整体替换成给定集合。
//
// 与 SetGroupMembers 同样的理由：整体替换（先清后写）比差分更新更能表达
// 「这是该组当前的完整授权」，也避免两个人同时编辑时互相把对方的改动冲掉。
func (d *DB) SetGroupGrants(ctx context.Context, groupID string, grants []user.GroupGrant) error {
	return d.Tx(ctx, func(tx *sqlTx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups WHERE id = ?`, groupID).Scan(&exists); err != nil {
			return fmt.Errorf("查询用户组失败: %w", err)
		}
		if exists == 0 {
			return ErrNotFound
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM group_agents WHERE group_id = ?`, groupID); err != nil {
			return fmt.Errorf("清空组授权失败: %w", err)
		}

		// 同组同一个 agent 只能有一条授权：入参里重复的 agent_id 取最后一次，
		// 顺序即调用方意图（前端通常按列表顺序渲染，用户最后改的那条最符合预期）。
		merged := make(map[string]user.Permission, len(grants))
		order := make([]string, 0, len(grants))
		for _, g := range grants {
			if _, ok := merged[g.AgentID]; !ok {
				order = append(order, g.AgentID)
			}
			merged[g.AgentID] = g.Permission
		}

		for _, agentID := range order {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO group_agents(group_id, agent_id, permission) VALUES(?, ?, ?)`,
				groupID, agentID, merged[agentID].String()); err != nil {
				return normalizeExecError(err, "写入组授权")
			}
		}
		return nil
	})
}

// CountGroupGrants 统计某组的授权数量（用于删除前的提示与列表展示）。
func (d *DB) CountGroupGrants(ctx context.Context, groupID string) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM group_agents WHERE group_id = ?`, groupID).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计组授权失败: %w", err)
	}
	return n, nil
}
