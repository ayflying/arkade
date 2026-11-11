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
	"encoding/json"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
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

	// DO 的字段是 any，未赋值的保持 nil、被 ORM 忽略，
	// 所以"本次改哪些字段"直接由赋值与否表达。
	data := do.Agents{}
	changed := false

	if fields.DirectAddress != nil {
		data.DirectAddress = *fields.DirectAddress
		changed = true
	}
	if fields.DisplayName != nil {
		data.DisplayName = *fields.DisplayName
		changed = true
	}
	if fields.Note != nil {
		data.Note = *fields.Note
		changed = true
	}
	if fields.Labels != nil {
		raw, mErr := json.Marshal(*fields.Labels)
		if mErr != nil {
			return AgentRecord{}, gerror.Wrap(mErr, "序列化 Agent 标签失败")
		}
		data.Labels = string(raw)
		changed = true
	}

	// 没有任何字段要改时直接返回当前记录，不发 UPDATE。
	// 避免产生一条"什么都没变"的写操作去推动 updated_at。
	if !changed {
		return d.GetAgent(ctx, id)
	}
	data.UpdatedAt = now

	if _, err := d.gdb.Model(dao.Agents.Table()).Ctx(ctx).
		Where(dao.Agents.Columns().Id, id).
		Data(data).
		Update(); err != nil {
		return AgentRecord{}, gerror.Wrap(err, "更新 Agent 信息失败")
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
	if _, err := d.gdb.Model(dao.Agents.Table()).Ctx(ctx).
		Where(dao.Agents.Columns().Id, id).
		Data(do.Agents{Disabled: v, UpdatedAt: now}).
		Update(); err != nil {
		return gerror.Wrap(err, "更新 Agent 禁用状态失败")
	}
	return nil
}

// IsAgentDisabled 查询 Agent 是否被禁用。
//
// 握手时会调用它，因此实现要尽量轻：只查一列，不加载整行。
// Agent 不存在时返回 (false, ErrNotFound)，由调用方决定如何处理
// （当前策略是允许首次注册）。
func (d *DB) IsAgentDisabled(ctx context.Context, id string) (bool, error) {
	c := dao.Agents.Columns()
	one, err := d.gdb.Model(dao.Agents.Table()).Ctx(ctx).
		Fields(c.Disabled).
		Where(c.Id, id).
		One()
	if err != nil {
		return false, gerror.Wrap(err, "查询 Agent 禁用状态失败")
	}
	if one.IsEmpty() {
		return false, ErrNotFound
	}
	return one[c.Disabled].Int() != 0, nil
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
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		c := dao.Agents.Columns()
		exists, err := tx.Model(dao.Agents.Table()).Ctx(ctx).
			Where(c.Id, id).
			Count()
		if err != nil {
			return gerror.Wrap(err, "查询 Agent 是否存在失败")
		}
		if exists == 0 {
			return ErrNotFound
		}
		if _, err := tx.Model(dao.Agents.Table()).Ctx(ctx).
			Where(c.Id, id).
			Delete(); err != nil {
			return gerror.Wrap(err, "删除 Agent 失败")
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
	n, err := d.gdb.Model(dao.ComposeStacks.Table()+" cs").Ctx(ctx).
		LeftJoin(dao.Agents.Table()+" a", "a.id = cs.agent_id").
		WhereNull("a.id").
		Count()
	if err != nil {
		return 0, gerror.Wrap(err, "统计孤儿编排失败")
	}
	return n, nil
}

// agentExists 判断 Agent 是否存在。
func (d *DB) agentExists(ctx context.Context, id string) (bool, error) {
	n, err := d.gdb.Model(dao.Agents.Table()).Ctx(ctx).
		Where(dao.Agents.Columns().Id, id).
		Count()
	if err != nil {
		return false, gerror.Wrap(err, "查询 Agent 是否存在失败")
	}
	return n > 0, nil
}
