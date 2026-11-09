package store

// agents_preprovision.go 实现「预创建 Agent 服务器记录」。
//
// 用户需求：在服务器列表里"添加服务器"，Hub 预建一条记录并下发一把
// 专属接入密钥；那台机器上的 Agent 用这把密钥连上来，就绑定到这条记录。
//
// 这与旧流程（先跑 Agent、Hub 自动建记录）互补而非替代：
// 旧记录由共享 token 照常接入，新记录必须持有专属密钥。

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StatusPending 表示记录已创建但从未有 Agent 连上来过。
//
// 为什么不复用 offline：offline 的含义是"曾经连上、现在断开"，
// 用户看到 offline 会去查为什么掉线（查网络、查进程）；
// 而 pending 的正确处置是"还没部署 Agent"。
// 把两者混成一个状态，会让全新未部署的服务器看起来像故障机器。
const StatusPending = "pending"

// ErrAgentNameTaken 表示已存在同名（规范化后）的服务器记录。
var ErrAgentNameTaken = errors.New("同名服务器已存在")

// NewAgentID 生成预创建记录的 ID。
//
// 用随机串而不是自增：预创建发生在管理员操作时，
// 而真正的 Agent 可能几天后才连上来，随机 ID 不依赖任何顺序或计数器，
// 也不会与旧流程按主机名生成的 ID 撞号。
func NewAgentID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机 ID 失败: %w", err)
	}
	return "agent-" + hex.EncodeToString(b), nil
}

// CreatePreprovisionedAgent 在事务内创建预建记录并写入凭据摘要。
//
// **必须在一个事务里完成两件事**（建记录 + 写凭据）：
// 若分开做，"记录已建但凭据没写"会留下一条既没有密钥、
// 又不是旧流程记录的孤儿行——它既不能被专属密钥接入，
// 又因为 preprovisioned=1 而拒绝共享 token，变成谁也连不上的死记录。
//
// 同名检查也在事务内：先查后插在同一事务里，才能挡住并发创建。
// 注意 SQLite 的写事务是串行的，因此这里的检查是可靠的；
// 注意**不给 agents.name 加全局唯一约束**——历史部署里存在
// 同名主机名的记录，加约束会让迁移直接失败。所以约束只加在
// "本次操作不得产生新重名"这一层。
func (d *DB) CreatePreprovisionedAgent(ctx context.Context, rec AgentRecord, tokenHash string, at time.Time) error {
	labelsRaw, err := json.Marshal(rec.Labels)
	if err != nil {
		return fmt.Errorf("序列化 Agent 标签失败: %w", err)
	}
	return d.Tx(ctx, func(tx *sqlTx) error {
		// 事务内判重（规范化后比较，见 normalizAgentName）。
		dup, err := agentNameExistsTx(ctx, tx, rec.Name, "")
		if err != nil {
			return err
		}
		if dup {
			return ErrAgentNameTaken
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agents(id, name, display_name, note, status, version, runtime,
			     runtime_version, last_seen, labels, created_at, updated_at, disabled, preprovisioned)
			 VALUES(?, ?, ?, ?, ?, '', '', '', 0, ?, ?, ?, 0, 1)`,
			rec.ID, rec.Name, rec.DisplayName, rec.Note, StatusPending,
			labelsRaw, rec.CreatedAt, rec.UpdatedAt); err != nil {
			return fmt.Errorf("写入预建服务器失败: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agent_tokens (token_hash, agent_id, created_at) VALUES ($1, $2, $3)`,
			tokenHash, rec.ID, at.Unix()); err != nil {
			return fmt.Errorf("写入接入凭据失败: %w", err)
		}
		return nil
	})
}

// ResetAgentTokenHash 替换某条记录的凭据摘要。
//
// 语义要求"旧的立即失效"：所以用 UPDATE 而不是追加新行，
// 并依赖 agent_id 上的唯一索引保证同时只有一个摘要。
// 记录不存在时返回 ErrNotFound（调用方据此 404）。
func (d *DB) ResetAgentTokenHash(ctx context.Context, agentID, tokenHash string, at time.Time) error {
	return d.Tx(ctx, func(tx *sqlTx) error {
		var one int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM agents WHERE id = ?`, agentID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("查询服务器失败: %w", err)
		}
		// 先删该 Agent 的所有旧摘要，再插入新的。
		// 不用 UPDATE 是因为也可能出现"该 Agent 还没有凭据行"的情况
		// （例如预建时写凭据失败后人工补建），DELETE + INSERT 两种都覆盖。
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM agent_tokens WHERE agent_id = $1`, agentID); err != nil {
			return fmt.Errorf("删除旧凭据失败: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO agent_tokens (token_hash, agent_id, created_at) VALUES ($1, $2, $3)`,
			tokenHash, agentID, at.Unix()); err != nil {
			return fmt.Errorf("写入新凭据失败: %w", err)
		}
		return nil
	})
}

// agentNameExistsTx 在事务内判断是否已有同名记录。
//
// excludeID 用于"改自己的名字"时把自己排除掉。
//
// 比较用规范化后的结果：历史数据里可能有大小写差异或首尾空格，
// 那条记录在界面上看起来就叫这个名字，用户再次输入同名时
// 应该在 UI 层就被拦住，而不是等到两条几乎一样的记录并排出现才发现。
func agentNameExistsTx(ctx context.Context, tx *sqlTx, name, excludeID string) (bool, error) {
	want := normalizAgentName(name)
	if want == "" {
		return false, nil
	}
	// SQLite 的 LOWER/TRIM 能覆盖大小写与空白差异，
	// 避免把所有记录捞到 Go 里比较。
	rows, err := tx.QueryContext(ctx,
		`SELECT id, name FROM agents WHERE id <> ?`, excludeID)
	if err != nil {
		return false, fmt.Errorf("查询同名服务器失败: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, existing string
		if err := rows.Scan(&id, &existing); err != nil {
			return false, fmt.Errorf("扫描服务器记录失败: %w", err)
		}
		if normalizAgentName(existing) == want {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("遍历服务器记录失败: %w", err)
	}
	return false, nil
}

// normalizAgentName 归一化名称用于比较（不影响存储值）。
//
// 只做去空白 + 折叠连续空白 + 小写。故意不做更激进的归一
// （比如去掉所有标点）：那会让「prod-1」和「prod_1」被视为同名，
// 而它们在 Compose 项目名语境下是完全不同的东西。
func normalizAgentName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
