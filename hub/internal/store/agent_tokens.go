package store

// agent_tokens.go 管理「服务器专属接入密钥」的摘要。
//
// 安全模型（这是本文件存在的全部理由）：
//
//	明文密钥只在创建/重置的响应里出现一次，数据库只存 SHA256。
//	Hub 永远无法反推出明文——即使整库被拖走，攻击者也拿不到能直接用的密钥。
//
// 与旧的"全局共享 token"的关系：
//   - 共享 token 明文在配置文件里（ARKADE_AGENT_TOKEN），所有机器共用一把；
//   - 专属 token 一机一把，按摘要精确解析出它绑定的 agent_id。
// 两者并存，但**共享 token 不允许认领已经拥有专属凭据的记录**，
// 否则"专属"就形同虚设（见 verifyAgentToken 的说明）。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrAgentTokenNotFound 表示该摘要没有对应的凭据。
var ErrAgentTokenNotFound = errors.New("接入凭据不存在")

// AgentToken 是凭据的元信息。
//
// 注意：**没有任何表示明文的字段**。这不是遗漏——
// 结构体里没有地方放明文，就不可能在代码里不小心把它传出去。
type AgentToken struct {
	// TokenHash 是 SHA256 的十六进制串。
	TokenHash string
	AgentID   string
	CreatedAt time.Time
}

// SaveAgentToken 写入或替换某个 Agent 的凭据摘要。
//
// 语义是"替换"而不是"新增"：一个 Agent 在同一时刻只能有一把有效密钥。
// 实现上用 DELETE + INSERT 而不是 UPSERT，因为 agent_id 上的唯一索引
// 决定了旧行必须先消失，否则插入会撞唯一约束。
//
// 整个操作在一个事务里：先删后插若只成功一半，
// 会留下"该 Agent 没有任何凭据"的状态——那样它就退化成可被
// 共享 token 认领的记录，是一次权限降级。
func (d *DB) SaveAgentToken(ctx context.Context, tokenHash, agentID string, at time.Time) error {
	if tokenHash == "" || agentID == "" {
		return errors.New("凭据摘要与 agent_id 均不能为空")
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 先删该 Agent 的旧凭据（重置语义）。
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_tokens WHERE agent_id = $1`, agentID); err != nil {
		return fmt.Errorf("删除旧凭据失败: %w", err)
	}
	// 再删可能存在的同摘要行（理论上不会，防御性处理：
	// 若同一摘要因某种原因绑到了别的 Agent，留着会造成歧义）。
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM agent_tokens WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("清理冲突凭据失败: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO agent_tokens (token_hash, agent_id, created_at) VALUES ($1, $2, $3)`,
		tokenHash, agentID, at.Unix()); err != nil {
		return fmt.Errorf("写入凭据失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}

// LookupAgentByTokenHash 按摘要解析出它绑定的 agent_id。
//
// 查不到返回 ErrAgentTokenNotFound——调用方据此区分
// "不是专属密钥"（可能是共享 token）与"数据库出错"。
// 这个区分很重要：前者走共享 token 的旧路径，后者必须 fail closed。
func (d *DB) LookupAgentByTokenHash(ctx context.Context, tokenHash string) (string, error) {
	if tokenHash == "" {
		return "", ErrAgentTokenNotFound
	}
	var agentID string
	err := d.db.QueryRowContext(ctx,
		`SELECT agent_id FROM agent_tokens WHERE token_hash = $1`, tokenHash).Scan(&agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrAgentTokenNotFound
	}
	if err != nil {
		return "", fmt.Errorf("查询凭据失败: %w", err)
	}
	return agentID, nil
}

// DeleteAgentToken 删除某个 Agent 的凭据（重置与删除记录时调用）。
//
// 删完立即生效：下一次握手按摘要查不到，旧密钥即刻作废。
// 不做"软删除保留一段时间"——那样旧密钥在保留期内仍然可用，
// 与"重置立即失效"的承诺不符。
func (d *DB) DeleteAgentToken(ctx context.Context, agentID string) error {
	if agentID == "" {
		return nil
	}
	if _, err := d.db.ExecContext(ctx,
		`DELETE FROM agent_tokens WHERE agent_id = $1`, agentID); err != nil {
		return fmt.Errorf("删除凭据失败: %w", err)
	}
	return nil
}

// HasAgentToken 判断某个 Agent 是否已配置专属凭据。
//
// 用于「共享 token 不得认领有专属凭据的记录」这条规则。
// 查询失败时返回 error 而不是 false：false 会让调用方放行，
// 那正是我们想避免的权限降级，所以必须让调用方 fail closed。
func (d *DB) HasAgentToken(ctx context.Context, agentID string) (bool, error) {
	if agentID == "" {
		return false, nil
	}
	var one int
	err := d.db.QueryRowContext(ctx,
		`SELECT 1 FROM agent_tokens WHERE agent_id = $1`, agentID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("查询凭据存在性失败: %w", err)
	}
	return true, nil
}
