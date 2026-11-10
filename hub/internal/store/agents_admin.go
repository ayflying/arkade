package store

// agents_admin.go 是 Agent（服务器）的管理操作：改名/备注/禁用/删除。
//
// 与 agents.go 分开的理由：agents.go 处理的是 Agent 自己上报的运行时状态
// （握手、心跳，写入频率高、字段全由 Agent 决定），而这里处理的是
// 管理员对纳管关系的修改（频率低、只动人工字段）。
// 两类写入的触发者、频率、字段归属都不一样，混在一起容易在不经意间
// 用 UpsertAgent 把用户改的显示名覆盖成 Agent 上报的主机名。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// AgentAdminFields 是管理员可编辑的字段。
//
// 全部用指针：nil 表示"本次不改这个字段"。
// 这是 PATCH 语义的必要条件——如果用零值表示"不改"，
// 就无法把 note 清空成空串（空串与"没传"不可区分）。
type AgentAdminFields struct {
	DirectAddress *string
	DisplayName   *string
	Note          *string
	Labels        *map[string]string
}

// UpdateAgentAdmin 更新管理员可编辑字段，返回更新后的记录。
//
// 只更新传入的字段，不动 Agent 自己上报的那些列（name/status/version/...）。
// 这条边界很重要：管理员改备注的同时如果 Agent 正好心跳，两个写入
// 不应互相覆盖——各写各的列，就不会丢数据。
func (d *DB) UpdateAgentAdmin(ctx context.Context, id string, fields AgentAdminFields, now int64) (AgentRecord, error) {
	exists, err := d.agentExists(ctx, id)
	if err != nil {
		return AgentRecord{}, err
	}
	if !exists {
		return AgentRecord{}, ErrNotFound
	}

	var sets []string
	var args []any

	if fields.DirectAddress != nil {
		sets = append(sets, "direct_address = ?")
		args = append(args, *fields.DirectAddress)
	}
	if fields.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *fields.DisplayName)
	}
	if fields.Note != nil {
		sets = append(sets, "note = ?")
		args = append(args, *fields.Note)
	}
	if fields.Labels != nil {
		raw, err := json.Marshal(*fields.Labels)
		if err != nil {
			return AgentRecord{}, fmt.Errorf("序列化 Agent 标签失败: %w", err)
		}
		sets = append(sets, "labels = ?")
		args = append(args, string(raw))
	}

	// 没有任何字段要改时直接返回当前记录，不发 UPDATE。
	// 避免产生一条"什么都没变"的写操作去推动 updated_at。
	if len(sets) == 0 {
		return d.GetAgent(ctx, id)
	}

	sets = append(sets, "updated_at = ?")
	args = append(args, now, id)
	q := "UPDATE agents SET " + joinComma(sets) + " WHERE id = ?"
	if _, err := d.db.ExecContext(ctx, q, args...); err != nil {
		return AgentRecord{}, fmt.Errorf("更新 Agent 信息失败: %w", err)
	}
	return d.GetAgent(ctx, id)
}

// SetAgentDisabled 设置禁用状态。
//
// 禁用只影响"能否建立连接"，不改变纳管关系与已有数据，
// 因此这是一个可逆操作——这正是它比删除更适合日常运维的原因。
func (d *DB) SetAgentDisabled(ctx context.Context, id string, disabled bool, now int64) error {
	exists, err := d.agentExists(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	v := 0
	if disabled {
		v = 1
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE agents SET disabled = ?, updated_at = ? WHERE id = ?`, v, now, id,
	); err != nil {
		return fmt.Errorf("更新 Agent 禁用状态失败: %w", err)
	}
	return nil
}

// IsAgentDisabled 查询 Agent 是否被禁用。
//
// 握手时会调用它，因此实现要尽量轻：只查一列，不加载整行。
// Agent 不存在时返回 (false, ErrNotFound)，由调用方决定如何处理
// （当前策略是允许首次注册）。
func (d *DB) IsAgentDisabled(ctx context.Context, id string) (bool, error) {
	var disabled int
	err := d.db.QueryRowContext(ctx, `SELECT disabled FROM agents WHERE id = ?`, id).Scan(&disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("查询 Agent 禁用状态失败: %w", err)
	}
	return disabled != 0, nil
}

// DeleteAgent 删除 Agent 记录。
//
// **刻意不删除 compose_stacks**——这是本功能最重要的边界。
//
// 理由：编排内容是用户手写的资产（可能花了几小时调试），
// 而 Agent 记录只是"这台机器归谁管"的登记。删掉登记不该连坐用户的文件。
// 更重要的是，Agent 可能只是暂时下线/换机重装，若连带删了编排，
// 用户会永久失去内容且无法察觉。删完留下的编排变成"孤儿"，
// 由界面的孤儿展示机制提示用户处理，这比静默删除安全得多。
//
// 同时也不删除 group_agents 里的授权（那是用户组对服务器的权限配置），
// 同样属于用户资产。
//
// 用事务包起来是为了保证"检查存在 → 删除"不被并发插入穿插，
// 也让将来若真的要连带清理关联数据时有明确的落点。
func (d *DB) DeleteAgent(ctx context.Context, id string) error {
	return d.Tx(ctx, func(tx *sqlTx) error {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ?`, id).Scan(&exists); err != nil {
			return fmt.Errorf("查询 Agent 是否存在失败: %w", err)
		}
		if exists == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id); err != nil {
			return fmt.Errorf("删除 Agent 失败: %w", err)
		}
		return nil
	})
}

// CountOrphanComposeStacks 统计"所属 Agent 已不存在"的编排数量。
//
// 提供给界面提示用户"有 N 条编排当前没有归属服务器"。
// 用 LEFT JOIN 而不是先查所有 agent_id 再比对：后者在编排多时
// 会产生大量往返，而这个查询在数据库里一次算完。
func (d *DB) CountOrphanComposeStacks(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM compose_stacks cs
		 LEFT JOIN agents a ON a.id = cs.agent_id
		 WHERE a.id IS NULL`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("统计孤儿编排失败: %w", err)
	}
	return n, nil
}

// agentExists 判断 Agent 是否存在。
func (d *DB) agentExists(ctx context.Context, id string) (bool, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ?`, id).Scan(&n); err != nil {
		return false, fmt.Errorf("查询 Agent 是否存在失败: %w", err)
	}
	return n > 0, nil
}

// joinComma 用逗号拼接 SQL 片段。
func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
