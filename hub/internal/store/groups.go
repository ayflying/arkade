package store

// groups.go 是用户组与「用户-组」成员关系的仓储层。
//
// 与 grants.go 的分工：本文件管「谁属于哪个组」，grants.go 管「这个组能碰哪些服务器」。
// 两者经常一起用，但变更原因不同（组织结构 vs 授权策略），因此分文件维护。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"arkade/hub/internal/user"
)

// GroupRecord 是用户组的一行记录，附带聚合计数。
//
// MemberCount / GrantCount 由单条聚合查询算出，而不是让上层再发两个 COUNT：
// 列表接口一次请求就要展示这两个数字，避免 N+1 查询。
type GroupRecord struct {
	ID          string
	Name        string
	Description string
	CreatedAt   time.Time
	MemberCount int
	GrantCount  int
}

// groupColumns 是组表查询字段的唯一定义，与 scanGroup 一一对应。
const groupColumns = `id, name, description, created_at`

// CreateGroup 创建用户组。名称重复时返回 ErrConflict（上层映射 409）。
func (d *DB) CreateGroup(ctx context.Context, grp user.Group) error {
	const q = `INSERT INTO groups(id, name, description, created_at) VALUES(?, ?, ?, ?)`
	_, err := d.db.ExecContext(ctx, q, grp.ID, grp.Name, grp.Description, timeToUnix(grp.CreatedAt))
	return normalizeExecError(err, "创建用户组")
}

// GetGroup 按 ID 查组。
func (d *DB) GetGroup(ctx context.Context, id string) (user.Group, error) {
	const q = `SELECT ` + groupColumns + ` FROM groups WHERE id = ?`
	var g user.Group
	var createdAt int64
	err := d.db.QueryRowContext(ctx, q, id).Scan(&g.ID, &g.Name, &g.Description, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return user.Group{}, ErrNotFound
	}
	if err != nil {
		return user.Group{}, fmt.Errorf("查询用户组失败: %w", err)
	}
	g.CreatedAt = unixToTime(createdAt)
	return g, nil
}

// ListGroups 列出全部用户组（含成员数与授权数），按创建时间升序。
//
// 用 LEFT JOIN + COUNT(DISTINCT ...) 一次算出两个计数：
// 若用两个子查询分别算，SQL 会写成两段相关子查询，可读性差且容易在
// 「一组成员但零授权」或反之的情况下漏算（JOIN 会放大行数，必须 DISTINCT）。
func (d *DB) ListGroups(ctx context.Context) ([]GroupRecord, error) {
	const q = `SELECT g.id, g.name, g.description, g.created_at,
	                  COUNT(DISTINCT ug.user_id) AS member_count,
	                  COUNT(DISTINCT ga.agent_id) AS grant_count
	           FROM groups g
	           LEFT JOIN user_groups ug ON ug.group_id = g.id
	           LEFT JOIN group_agents ga ON ga.group_id = g.id
	           GROUP BY g.id, g.name, g.description, g.created_at
	           ORDER BY g.created_at ASC, g.id ASC`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("查询用户组列表失败: %w", err)
	}
	defer rows.Close()

	out := make([]GroupRecord, 0, 16)
	for rows.Next() {
		var rec GroupRecord
		var createdAt int64
		if err := rows.Scan(&rec.ID, &rec.Name, &rec.Description, &createdAt,
			&rec.MemberCount, &rec.GrantCount); err != nil {
			return nil, fmt.Errorf("扫描用户组记录失败: %w", err)
		}
		rec.CreatedAt = unixToTime(createdAt)
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GroupUpdate 是用户组的局部更新参数（指针语义见 UserUpdate 的说明）。
type GroupUpdate struct {
	Name        *string
	Description *string
}

// UpdateGroup 更新组名与描述。组名重复返回 ErrConflict。
func (d *DB) UpdateGroup(ctx context.Context, id string, upd GroupUpdate) error {
	sets := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if upd.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *upd.Name)
	}
	if upd.Description != nil {
		sets = append(sets, "description = ?")
		args = append(args, *upd.Description)
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)

	q := `UPDATE groups SET ` + joinSet(sets) + ` WHERE id = ?`
	res, err := d.db.ExecContext(ctx, q, args...)
	if err != nil {
		return normalizeExecError(err, "更新用户组")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteGroup 删除用户组。
//
// 与外键级联的关系同 DeleteUser：显式清理 user_groups 与 group_agents，
// 不依赖连接级 PRAGMA 一定生效。删除组不会删除其成员用户——
// 组只是授权载体，用户是独立实体。
func (d *DB) DeleteGroup(ctx context.Context, id string) error {
	return d.Tx(ctx, func(tx *sqlTx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_groups WHERE group_id = ?`, id); err != nil {
			return fmt.Errorf("清理组成员关系失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM group_agents WHERE group_id = ?`, id); err != nil {
			return fmt.Errorf("清理组授权失败: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM groups WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除用户组失败: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// AddUserToGroup 把用户加入组。重复加入视为成功（幂等），不报冲突。
//
// 为什么幂等：整体设置成员的接口会反复下发同一份成员列表，
// 把「已存在」当错误会让前端的重试逻辑变得极其啰嗦。
func (d *DB) AddUserToGroup(ctx context.Context, userID, groupID string) error {
	const q = `INSERT INTO user_groups(user_id, group_id) SELECT ?, ? WHERE NOT EXISTS (
	               SELECT 1 FROM user_groups WHERE user_id = ? AND group_id = ?)`
	if _, err := d.db.ExecContext(ctx, q, userID, groupID, userID, groupID); err != nil {
		return fmt.Errorf("加入用户组失败: %w", err)
	}
	return nil
}

// RemoveUserFromGroup 把用户移出组。用户本就不在组里时视为成功。
func (d *DB) RemoveUserFromGroup(ctx context.Context, userID, groupID string) error {
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM user_groups WHERE user_id = ? AND group_id = ?`, userID, groupID); err != nil {
		return fmt.Errorf("移出用户组失败: %w", err)
	}
	return nil
}

// SetGroupMembers 原子地把组成员设置成给定集合。
//
// 为什么需要「整体设置」而不是让前端算增删：前端拿到的是旧列表，两次请求之间
// 其他人可能改过成员，按差集增删会把别人的改动冲掉。整体设置在一个事务里
// 先清后插，语义确定且不会出现中间态。
//
// 注意：调用方必须保证 userIDs 里的用户真实存在（httpx 层会先做存在性校验），
// 这里不做校验是为了避免在事务里再跑一轮查询。
func (d *DB) SetGroupMembers(ctx context.Context, groupID string, userIDs []string) error {
	return d.Tx(ctx, func(tx *sqlTx) error {
		// 先确认组存在，避免给不存在的组「塞」成员后返回成功。
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups WHERE id = ?`, groupID).Scan(&exists); err != nil {
			return fmt.Errorf("查询用户组失败: %w", err)
		}
		if exists == 0 {
			return ErrNotFound
		}

		if _, err := tx.ExecContext(ctx, `DELETE FROM user_groups WHERE group_id = ?`, groupID); err != nil {
			return fmt.Errorf("清空组成员失败: %w", err)
		}
		// 去重：同一用户在一个组里只能有一条关系（主键约束），
		// 提前去重可以避免把「调用方传了重复 ID」变成 500。
		seen := make(map[string]struct{}, len(userIDs))
		for _, uid := range userIDs {
			if _, dup := seen[uid]; dup {
				continue
			}
			seen[uid] = struct{}{}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO user_groups(user_id, group_id) VALUES(?, ?)`, uid, groupID); err != nil {
				return normalizeExecError(err, "设置组成员")
			}
		}
		return nil
	})
}

// GroupMember 是组成员视图，带上用户基本信息，省得前端再查一次用户表。
type GroupMember struct {
	ID          string
	Username    string
	DisplayName string
	Role        user.Role
	Disabled    bool
}

// ListGroupMembers 列出某个组的成员，按用户名升序（界面顺序稳定）。
func (d *DB) ListGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error) {
	const q = `SELECT u.id, u.username, u.display_name, u.role, u.disabled
	           FROM user_groups ug
	           JOIN users u ON u.id = ug.user_id
	           WHERE ug.group_id = ?
	           ORDER BY u.username ASC`
	rows, err := d.db.QueryContext(ctx, q, groupID)
	if err != nil {
		return nil, fmt.Errorf("查询组成员失败: %w", err)
	}
	defer rows.Close()

	out := make([]GroupMember, 0, 16)
	for rows.Next() {
		var m GroupMember
		var disabled int
		var role string
		if err := rows.Scan(&m.ID, &m.Username, &m.DisplayName, &role, &disabled); err != nil {
			return nil, fmt.Errorf("扫描组成员失败: %w", err)
		}
		m.Role = user.Role(role)
		m.Disabled = disabled != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListGroupsByUser 列出某用户所属的组（按组名排序）。
//
// 用户列表接口需要「每个用户属于哪些组」，逐个用户调用会产生 N+1；
// 因此另有 ListUserGroupIDs 供批量场景使用，本方法面向单个用户的详情场景。
func (d *DB) ListGroupsByUser(ctx context.Context, userID string) ([]user.Group, error) {
	const q = `SELECT g.id, g.name, g.description, g.created_at
	           FROM user_groups ug
	           JOIN groups g ON g.id = ug.group_id
	           WHERE ug.user_id = ?
	           ORDER BY g.name ASC`
	rows, err := d.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("查询用户所属组失败: %w", err)
	}
	defer rows.Close()

	out := make([]user.Group, 0, 8)
	for rows.Next() {
		var g user.Group
		var createdAt int64
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &createdAt); err != nil {
			return nil, fmt.Errorf("扫描用户所属组失败: %w", err)
		}
		g.CreatedAt = unixToTime(createdAt)
		out = append(out, g)
	}
	return out, rows.Err()
}

// ListUserGroupIDs 一次性查出「用户 → 组 ID 列表」的映射，供用户列表接口避免 N+1。
//
// 返回 map 而不是切片：调用方按 user ID 直接索引，缺失的用户自然得到 nil。
func (d *DB) ListUserGroupIDs(ctx context.Context) (map[string][]string, error) {
	const q = `SELECT ug.user_id, ug.group_id
	           FROM user_groups ug
	           JOIN groups g ON g.id = ug.group_id
	           ORDER BY ug.user_id ASC, g.name ASC`
	rows, err := d.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("查询用户组关系失败: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]string, 32)
	for rows.Next() {
		var uid, gid string
		if err := rows.Scan(&uid, &gid); err != nil {
			return nil, fmt.Errorf("扫描用户组关系失败: %w", err)
		}
		out[uid] = append(out[uid], gid)
	}
	return out, rows.Err()
}

// GroupExists 判断组是否存在，供整体设置接口做前置校验（返回 404 而非 400）。
func (d *DB) GroupExists(ctx context.Context, id string) (bool, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM groups WHERE id = ?`, id).Scan(&n); err != nil {
		return false, fmt.Errorf("查询用户组是否存在失败: %w", err)
	}
	return n > 0, nil
}

// UsersMissing 返回给定 ID 中不存在的那些，供「整体设置成员」做存在性校验。
//
// 返回缺失列表而不是 bool，是为了让错误信息能具体告诉调用方「哪几个用户不存在」，
// 而不是笼统的「参数错误」。去重后逐个查询：入参来自一次 HTTP 请求，
// 数量级是几十个，用 IN (...) 动态拼接反而要处理占位符数量与空列表两种边界。
func (d *DB) UsersMissing(ctx context.Context, ids []string) ([]string, error) {
	missing := make([]string, 0, len(ids))
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		var n int
		if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, id).Scan(&n); err != nil {
			return nil, fmt.Errorf("查询用户是否存在失败: %w", err)
		}
		if n == 0 {
			missing = append(missing, id)
		}
	}
	return missing, nil
}
