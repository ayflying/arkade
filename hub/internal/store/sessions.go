package store

// sessions.go 是登录会话的仓储层。
//
// 安全约定：数据库里只存令牌的 SHA-256 哈希，不存明文令牌。
// 即使数据库文件泄漏，攻击者也无法从中直接得到可用的会话 Cookie。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// CreateSession 写入一条会话记录。tokenHash 由 auth 层计算后传入。
func (d *DB) CreateSession(ctx context.Context, tokenHash, userID string, createdAt, expiresAt int64) error {
	const q = `INSERT INTO sessions(token_hash, user_id, created_at, expires_at) VALUES(?, ?, ?, ?)`
	if _, err := d.db.ExecContext(ctx, q, tokenHash, userID, createdAt, expiresAt); err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	return nil
}

// SessionUserID 按令牌哈希查会话对应的用户 ID。
// 过期会话视为不存在，同时顺手删除，避免表无限增长。
func (d *DB) SessionUserID(ctx context.Context, tokenHash string, now int64) (string, error) {
	var userID string
	var expiresAt int64
	err := d.db.QueryRowContext(ctx,
		`SELECT user_id, expires_at FROM sessions WHERE token_hash = ?`, tokenHash,
	).Scan(&userID, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("查询会话失败: %w", err)
	}

	if expiresAt <= now {
		// 过期即删除：惰性清理，无需额外定时任务。
		_ = d.DeleteSession(ctx, tokenHash)
		return "", ErrNotFound
	}
	return userID, nil
}

// DeleteSession 删除单个会话（退出登录）。
func (d *DB) DeleteSession(ctx context.Context, tokenHash string) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash); err != nil {
		return fmt.Errorf("删除会话失败: %w", err)
	}
	return nil
}

// DeleteExpiredSessions 清理全部过期会话，供启动时或定时任务调用。
func (d *DB) DeleteExpiredSessions(ctx context.Context, now int64) (int64, error) {
	res, err := d.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now)
	if err != nil {
		return 0, fmt.Errorf("清理过期会话失败: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
