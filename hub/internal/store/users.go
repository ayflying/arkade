// Package store 里用户相关的仓储实现。
//
// 文件划分（按职责，不按方法数量）：
//   - users.go     用户自身的读写（含唯一约束冲突归一化）
//   - groups.go    用户组自身 + 成员关系（user_groups）
//   - grants.go    组对服务器的授权（group_agents）
//
// 边界：本包只做持久化与约束冲突翻译，不做权限语义判断
// （例如「最后一个管理员不能降级」属于业务规则，放在 httpx 层）。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"arkade/hub/internal/user"
)

// userColumns 是用户表查询字段的唯一定义。
//
// 所有用户查询都从这一份拼接，避免「两个函数字段顺序不一致导致 Scan 错位」——
// 这类 bug 一旦出现会非常隐蔽（role 读到 display_name 里还不报错）。
const userColumns = `id, username, display_name, password_hash, role, disabled, created_at, updated_at`

// GetUserByUsername 按用户名查用户。登录流程的唯一查询入口。
func (d *DB) GetUserByUsername(ctx context.Context, username string) (user.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE username = ?`
	return d.scanUser(ctx, q, username)
}

// GetUserByID 按 ID 查用户。会话校验时使用。
func (d *DB) GetUserByID(ctx context.Context, id string) (user.User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE id = ?`
	return d.scanUser(ctx, q, id)
}

// scanUser 是单行用户查询的唯一扫描实现，所有单行查询共用。
func (d *DB) scanUser(ctx context.Context, query string, arg any) (user.User, error) {
	var u user.User
	var disabled int
	var createdAt, updatedAt int64
	err := d.db.QueryRowContext(ctx, query, arg).Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &disabled, &createdAt, &updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return user.User{}, ErrNotFound
	}
	if err != nil {
		return user.User{}, fmt.Errorf("查询用户失败: %w", err)
	}
	u.Disabled = disabled != 0
	u.CreatedAt = unixToTime(createdAt)
	u.UpdatedAt = unixToTime(updatedAt)
	return u, nil
}

// scanUserRows 从结果集扫描一行，字段顺序必须与 userColumns 一致。
func scanUserRows(rows *sql.Rows) (user.User, error) {
	var u user.User
	var disabled int
	var createdAt, updatedAt int64
	if err := rows.Scan(
		&u.ID, &u.Username, &u.DisplayName, &u.PasswordHash, &u.Role, &disabled, &createdAt, &updatedAt,
	); err != nil {
		return user.User{}, err
	}
	u.Disabled = disabled != 0
	u.CreatedAt = unixToTime(createdAt)
	u.UpdatedAt = unixToTime(updatedAt)
	return u, nil
}

// CreateUser 创建用户。调用方必须已完成密码哈希与用户名规整（见 user.NormalizeUsername）。
//
// 用户名重复时返回可被 errors.Is(err, ErrConflict) 识别的错误，上层映射成 409，
// 而不是把驱动的 "UNIQUE constraint failed" 原文甩给前端。
func (d *DB) CreateUser(ctx context.Context, u user.User) error {
	const q = `INSERT INTO users(id, username, display_name, password_hash, role, disabled, created_at, updated_at)
	           VALUES(?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := d.db.ExecContext(ctx, q,
		u.ID, u.Username, u.DisplayName, u.PasswordHash, string(u.Role), boolToInt(u.Disabled),
		timeToUnix(u.CreatedAt), timeToUnix(u.UpdatedAt),
	)
	return normalizeExecError(err, "创建用户")
}

// CountUsers 统计用户数量，供「首次启动是否要创建管理员」判断。
func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计用户数失败: %w", err)
	}
	return n, nil
}

// CountActiveAdmins 统计「有效管理员」数量：角色为 admin 且未被禁用。
//
// 这是「不能让最后一个管理员消失」的判定依据。单独成方法而不是让上层
// ListUsers + 自己过滤，是为了让这条规则只有一种实现口径——一旦两处口径不同，
// 就会出现「界面说还有一个管理员、实际删除时却说没有」的矛盾。
//
// excludeID 非空时不计入该用户（用于「如果这次操作会把这个人降级/删除，还剩几个」）。
func (d *DB) CountActiveAdmins(ctx context.Context, excludeID string) (int, error) {
	const q = `SELECT COUNT(*) FROM users WHERE role = ? AND disabled = 0 AND id <> ?`
	var n int
	if err := d.db.QueryRowContext(ctx, q, string(user.RoleAdmin), excludeID).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计管理员数量失败: %w", err)
	}
	return n, nil
}

// ListUsers 列出全部用户，按创建时间升序（同时间的按 id 兜底，保证顺序稳定）。
//
// 返回带 PasswordHash 的完整模型仅供内部使用：HTTP 层必须用视图结构转换，
// 绝不能直接序列化 user.User（PasswordHash 已有 json:"-" 标签，但不能只靠它兜底）。
func (d *DB) ListUsers(ctx context.Context) ([]user.User, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+userColumns+` FROM users ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询用户列表失败: %w", err)
	}
	defer rows.Close()

	out := make([]user.User, 0, 32)
	for rows.Next() {
		u, serr := scanUserRows(rows)
		if serr != nil {
			return nil, fmt.Errorf("扫描用户记录失败: %w", serr)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UserUpdate 是用户资料的局部更新参数。
//
// 用指针字段表达「本次要不要改这个字段」，而不是传零值。
// 为什么必须这样：空字符串与 false 都是合法输入（清空显示名虽然会被校验拦住，
// 但 disabled=false 是完全合法的「启用」操作），用零值判断会把「不修改」和
// 「改成零值」混为一谈。
type UserUpdate struct {
	DisplayName *string
	Role        *user.Role
	Disabled    *bool
}

// UpdateUser 更新用户的可变字段（显示名 / 角色 / 禁用状态）。
//
// 所有写入都放在一次 UPDATE 里完成，避免「先改角色成功、再改禁用失败」留下半截状态。
// 用户名与密码哈希不在此更新：前者涉及唯一约束与登录记忆，后者有独立的改密流程。
func (d *DB) UpdateUser(ctx context.Context, id string, upd UserUpdate, updatedAt int64) error {
	// 动态拼 SET 片段是必要的：只有本次真正要改的字段才会被写，
	// 这样并发修改不同字段时不会互相覆盖。
	sets := make([]string, 0, 4)
	args := make([]any, 0, 5)

	if upd.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *upd.DisplayName)
	}
	if upd.Role != nil {
		sets = append(sets, "role = ?")
		args = append(args, string(*upd.Role))
	}
	if upd.Disabled != nil {
		sets = append(sets, "disabled = ?")
		args = append(args, boolToInt(*upd.Disabled))
	}
	if len(sets) == 0 {
		// 没有任何变更就不写库：空 UPDATE 会白白推进一个写事务，
		// 在 SQLite 单写连接下等于无谓地串行化其他请求。
		return nil
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, updatedAt, id)

	q := `UPDATE users SET ` + joinSet(sets) + ` WHERE id = ?`
	res, err := d.db.ExecContext(ctx, q, args...)
	if err != nil {
		return normalizeExecError(err, "更新用户")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateUserPassword 重置密码。passwordHash 必须由 user.HashPassword 生成。
//
// 同时删除该用户的全部会话：改密码的常见动机就是「账号可能泄漏」，
// 若旧会话继续有效，改密就失去了意义。
func (d *DB) UpdateUserPassword(ctx context.Context, id, passwordHash string, updatedAt int64) error {
	return d.Tx(ctx, func(tx *sqlTx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`,
			passwordHash, updatedAt, id)
		if err != nil {
			return fmt.Errorf("重置密码失败: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return fmt.Errorf("清理用户会话失败: %w", err)
		}
		return nil
	})
}

// DeleteUser 删除用户。
//
// 关于级联删除：Open 时已通过 DSN 的 _pragma=foreign_keys(1) 为每条连接开启外键约束
// （store.go 有说明），users 被 user_groups 与 sessions 以 ON DELETE CASCADE 引用，
// 因此关联记录会自动清理。这里仍然显式删一次关联记录，原因是：
//   - 外键开关是连接级状态，若将来有人改 DSN 或换驱动，级联会静默失效并留下孤儿行；
//   - 显式删除是幂等的，与外键同时生效不会冲突，但能把「依赖隐式行为」变成「代码里写明的意图」。
func (d *DB) DeleteUser(ctx context.Context, id string) error {
	return d.Tx(ctx, func(tx *sqlTx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_groups WHERE user_id = ?`, id); err != nil {
			return fmt.Errorf("清理用户组成员关系失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id); err != nil {
			return fmt.Errorf("清理用户会话失败: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("删除用户失败: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// joinSet 用逗号拼接 SET 片段。放在 users.go 内部而不是 util.go，
// 因为它只服务于动态 UPDATE 这一种场景，util.go 约定只放跨文件的纯工具。
func joinSet(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
