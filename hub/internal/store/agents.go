package store

// agents.go 是 Agent 记录的仓储层。
//
// 职责边界：这里只做持久化，不做在线状态判断。
// 「在线」是连接层实时掌握的，数据库里的 status 只是最后快照，用于重启后展示。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// AgentRecord 是 agents 表的一行。
type AgentRecord struct {
	ID             string
	Name           string
	Status         string
	Version        string
	Runtime        string
	RuntimeVersion string
	LastSeen       int64
	Labels         map[string]string
	CreatedAt      int64
	UpdatedAt      int64

	// DisplayName 是用户可编辑的显示名，空串表示"未设置"。
	//
	// 与 Name 分开的原因见迁移版本 4 的注释：Name 是 Agent 自报的主机名，
	// 每次重连都会被覆盖；DisplayName 是人起的名字，必须由用户独占修改权。
	DisplayName string
	// Note 是自由备注，给人读，不参与任何筛选或权限判断。
	Note string
	// DirectAddress是管理员指定的浏览器访问主机，不参与Agent连接和服务端网络请求。
	DirectAddress string
	// Disabled 为真时，该 Agent 的握手请求会被拒绝。
	Disabled bool

	// Preprovisioned 表示这条记录是管理员预建的（先建记录、后部署机器）。
	//
	// 为什么单独用一个列，而不是"有没有凭据行"推导：
	// 重置密钥时会先删旧凭据行再插新的，那一瞬间凭据行是不存在的；
	// 若用"没有凭据行"来推断"不是专属服务器"，
	// 这条记录会在那个窗口里暂时变成可被**共享 token 认领**的状态——
	// 那是提权。标记必须独立持久化。
	Preprovisioned bool
}

// 列清单常量：集中在一处，避免 List 与 Get 两处的列顺序不一致
// 导致 Scan 参数错位（这类 bug 编译期发现不了，只在运行时把字段串到一起）。
const agentColumns = `id, name, status, version, runtime, runtime_version, last_seen, labels,
	created_at, updated_at, display_name, note, disabled, preprovisioned, direct_address`

// scanAgent 把一行扫描进 AgentRecord。
//
// 抽出来是为了让 ListAgents 与 GetAgent 共用同一份列顺序，
// 将来加字段只需改这一处。
func scanAgent(sc interface {
	Scan(dest ...any) error
}) (AgentRecord, error) {
	var rec AgentRecord
	var labelsRaw string
	var disabled int
	var preprovisioned int
	if err := sc.Scan(&rec.ID, &rec.Name, &rec.Status, &rec.Version, &rec.Runtime,
		&rec.RuntimeVersion, &rec.LastSeen, &labelsRaw, &rec.CreatedAt, &rec.UpdatedAt,
		&rec.DisplayName, &rec.Note, &disabled, &preprovisioned, &rec.DirectAddress); err != nil {
		return AgentRecord{}, err
	}
	rec.Labels = parseLabels(labelsRaw)
	rec.Disabled = disabled != 0
	rec.Preprovisioned = preprovisioned != 0
	return rec, nil
}

// UpsertAgent 写入或更新 Agent 记录（握手与心跳时调用）。
//
// 使用「先查后改」而不是 INSERT OR REPLACE：
// 后者是 SQLite 专有语法，且会重建整行（触发级联删除），不利于后续迁移数据库。
func (d *DB) UpsertAgent(ctx context.Context, rec AgentRecord) error {
	labelsRaw, err := json.Marshal(rec.Labels)
	if err != nil {
		return fmt.Errorf("序列化 Agent 标签失败: %w", err)
	}

	return d.Tx(ctx, func(tx *sqlTx) error {
		var exists int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agents WHERE id = ?`, rec.ID).Scan(&exists)
		if err != nil {
			return fmt.Errorf("查询 Agent 是否存在失败: %w", err)
		}

		if exists == 0 {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO agents(id, name, status, version, runtime, runtime_version, last_seen, labels, created_at, updated_at)
				 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				rec.ID, rec.Name, rec.Status, rec.Version, rec.Runtime, rec.RuntimeVersion,
				rec.LastSeen, string(labelsRaw), rec.CreatedAt, rec.UpdatedAt,
			)
			if err != nil {
				return fmt.Errorf("写入 Agent 失败: %w", err)
			}
			return nil
		}

		// 握手仅刷新运行状态；标签与显示名/备注由管理员维护，重连不得清空。
		_, err = tx.ExecContext(ctx,
			`UPDATE agents SET name = ?, status = ?, version = ?, runtime = ?, runtime_version = ?,
			        last_seen = ?, updated_at = ?
			 WHERE id = ?`,
			rec.Name, rec.Status, rec.Version, rec.Runtime, rec.RuntimeVersion,
			rec.LastSeen, rec.UpdatedAt, rec.ID,
		)
		if err != nil {
			return fmt.Errorf("更新 Agent 失败: %w", err)
		}
		return nil
	})
}

// ListAgents 返回全部 Agent，按创建时间升序，保证界面顺序稳定。
func (d *DB) ListAgents(ctx context.Context) ([]AgentRecord, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+agentColumns+` FROM agents ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("查询 Agent 列表失败: %w", err)
	}
	defer rows.Close()

	var out []AgentRecord
	for rows.Next() {
		rec, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描 Agent 记录失败: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetAgent 按 ID 查单个 Agent。
func (d *DB) GetAgent(ctx context.Context, id string) (AgentRecord, error) {
	row := d.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE id = ?`, id)
	rec, err := scanAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return AgentRecord{}, ErrNotFound
	}
	if err != nil {
		return AgentRecord{}, fmt.Errorf("查询 Agent 失败: %w", err)
	}
	return rec, nil
}

// UpdateAgentStatus 只更新在线状态与最后心跳时间，供心跳高频调用使用。
// 单独拆出来是为了避免心跳每次都走完整 Upsert（会重写所有字段）。
func (d *DB) UpdateAgentStatus(ctx context.Context, id, status string, lastSeen int64) error {
	if _, err := d.db.ExecContext(ctx,
		`UPDATE agents SET status = ?, last_seen = ?, updated_at = ? WHERE id = ?`,
		status, lastSeen, lastSeen, id,
	); err != nil {
		return fmt.Errorf("更新 Agent 状态失败: %w", err)
	}
	return nil
}

// parseLabels 解析标签 JSON。脏数据不应让整个列表接口失败，
// 因此解析失败时降级为空标签并继续。
func parseLabels(raw string) map[string]string {
	out := map[string]string{}
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]string{}
	}
	if out == nil {
		out = map[string]string{}
	}
	return out
}
