// Package store 里用户相关的仓储实现（查询部分）。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"arkade/hub/internal/user"
)

// ErrNotFound 是统一的「记录不存在」错误。
// 用哨兵错误而不是把 sql.ErrNoRows 泄漏到上层，避免各层重复判断驱动细节。
var ErrNotFound = errors.New("记录不存在")

// GetUserByUsername 按用户名查用户。登录流程的唯一查询入口。
func (d *DB) GetUserByUsername(ctx context.Context, username string) (user.User, error) {
	const q = `SELECT id, username, display_name, password_hash, role, disabled, created_at, updated_at
	           FROM users WHERE username = ?`
	return d.scanUser(ctx, q, username)
}

// GetUserByID 按 ID 查用户。会话校验时使用。
func (d *DB) GetUserByID(ctx context.Context, id string) (user.User, error) {
	const q = `SELECT id, username, display_name, password_hash, role, disabled, created_at, updated_at
	           FROM users WHERE id = ?`
	return d.scanUser(ctx, q, id)
}

// scanUser 是用户查询的唯一扫描实现，两个查询共用，避免字段顺序写两遍导致错位。
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

// CreateUser 创建用户。调用方必须已完成密码哈希与用户名规整。
func (d *DB) CreateUser(ctx context.Context, u user.User) error {
	const q = `INSERT INTO users(id, username, display_name, password_hash, role, disabled, created_at, updated_at)
	           VALUES(?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := d.db.ExecContext(ctx, q,
		u.ID, u.Username, u.DisplayName, u.PasswordHash, string(u.Role), boolToInt(u.Disabled),
		u.CreatedAt.Unix(), u.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("创建用户失败: %w", err)
	}
	return nil
}

// CountUsers 统计用户数量，供「首次启动是否要创建管理员」判断。
func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计用户数失败: %w", err)
	}
	return n, nil
}

// ListGroupGrants 查询某用户通过所有用户组获得的服务器授权。
//
// 返回的是逐条记录而不是合并后的最大权限：合并逻辑属于权限判定层，
// 放在这里会让存储层承担语义判断，不利于测试。
func (d *DB) ListGroupGrants(ctx context.Context, userID string) ([]user.GroupGrant, error) {
	const q = `SELECT ga.group_id, ga.agent_id, ga.permission
	           FROM group_agents ga
	           JOIN user_groups ug ON ug.group_id = ga.group_id
	           WHERE ug.user_id = ?`
	rows, err := d.db.QueryContext(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("查询组授权失败: %w", err)
	}
	defer rows.Close()

	var out []user.GroupGrant
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
