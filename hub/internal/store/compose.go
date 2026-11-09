package store

// compose.go 是容器编排项目（compose stack）的仓储层。
//
// 职责边界：
//   - 只负责 Hub 侧权威副本的读写与乐观锁，不碰 Agent、不执行 compose（那是连接层的事）；
//   - 不判断项目名合法性（由 httpx 层用统一规则校验，存储层只存）；
//   - 不做哈希计算（由调用方传入，保证「Hub 算的哈希」与「Agent 算的哈希」用同一算法）。
//
// 命名注意：本文件的方法一律以 ComposeStack 为主体（CreateComposeStack 等），
// 不再出现「GetGroupGrants 收 userID」那种同名不同义的情况——
// 项目里已经因为这种命名吃过一次权限静默失效的亏。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ComposeStack 是 compose_stacks 表的一行。
type ComposeStack struct {
	ID         string
	AgentID    string
	Name       string
	Content    string
	EnvContent string
	ConfigHash string
	// Version 是乐观锁版本号：每次内容更新 +1。客户端提交更新时必须带上
	// 它读到的版本，版本不匹配说明期间有人改过，必须让用户重新确认。
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
	// SyncedAt 是最近一次成功下发到 Agent 的时间，零值表示从未成功下发。
	//
	// 与 UpdatedAt 分开的原因：用户可以在 Agent 离线时照常编辑并保存，
	// 此时 UpdatedAt 会变而 SyncedAt 不变。界面据此提示"已保存、尚未下发"，
	// 用户才不会误以为线上已经生效。
	SyncedAt time.Time
}

// composeStackColumns 是查询字段的唯一定义，与 scanComposeStack 一一对应。
const composeStackColumns = `id, agent_id, name, content, env_content, config_hash, version, created_at, updated_at, synced_at`

// CreateComposeStack 插入一条编排记录。
//
// 同一 Agent 下项目名重复时返回 ErrConflict（上层映射 409）：项目名就是 Agent
// 本机的目录名，重名会让两份编排互相覆盖文件，必须在入库这一层就拦住。
func (d *DB) CreateComposeStack(ctx context.Context, s ComposeStack) error {
	const q = `INSERT INTO compose_stacks(id, agent_id, name, content, env_content, config_hash, version, created_at, updated_at, synced_at)
	           VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := d.db.ExecContext(ctx, q,
		s.ID, s.AgentID, s.Name, s.Content, s.EnvContent, s.ConfigHash, s.Version,
		timeToUnix(s.CreatedAt), timeToUnix(s.UpdatedAt), timeToUnix(s.SyncedAt),
	)
	return normalizeExecError(err, "创建编排")
}

// scanComposeStack 是单行扫描的唯一实现，所有单行查询共用。
func scanComposeStack(row interface {
	Scan(dest ...any) error
}) (ComposeStack, error) {
	var s ComposeStack
	var createdAt, updatedAt, syncedAt int64
	err := row.Scan(&s.ID, &s.AgentID, &s.Name, &s.Content, &s.EnvContent, &s.ConfigHash, &s.Version,
		&createdAt, &updatedAt, &syncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ComposeStack{}, ErrNotFound
	}
	if err != nil {
		return ComposeStack{}, fmt.Errorf("查询编排失败: %w", err)
	}
	s.CreatedAt = unixToTime(createdAt)
	s.UpdatedAt = unixToTime(updatedAt)
	s.SyncedAt = unixToTime(syncedAt)
	return s, nil
}

// GetComposeStack 按 (agentID, name) 查编排。
//
// 用「Agent + 项目名」而不是主键 id 作为查询条件：对外所有接口的路径都是
// /agents/{agentID}/stacks/{name}，项目名才是用户视角的标识；id 只在内部用。
func (d *DB) GetComposeStack(ctx context.Context, agentID, name string) (ComposeStack, error) {
	q := `SELECT ` + composeStackColumns + ` FROM compose_stacks WHERE agent_id = ? AND name = ?`
	return scanComposeStack(d.db.QueryRowContext(ctx, q, agentID, name))
}

// ListComposeStacks 列出某台 Agent 的全部编排，按项目名升序（界面顺序稳定）。
//
// agentID 为空时返回空列表而不是报错：调用方只会在已知 Agent 的上下文里调用，
// 空值属于编程错误，但没必要为此让接口 500。
func (d *DB) ListComposeStacks(ctx context.Context, agentID string) ([]ComposeStack, error) {
	q := `SELECT ` + composeStackColumns + ` FROM compose_stacks WHERE agent_id = ? ORDER BY name ASC`
	rows, err := d.db.QueryContext(ctx, q, agentID)
	if err != nil {
		return nil, fmt.Errorf("查询编排列表失败: %w", err)
	}
	defer rows.Close()

	out := make([]ComposeStack, 0, 16)
	for rows.Next() {
		s, serr := scanComposeStack(rows)
		if serr != nil {
			// rows 的 Scan 不会返回 sql.ErrNoRows，这里只需包装错误。
			return nil, fmt.Errorf("扫描编排记录失败: %w", serr)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpdateComposeStack 以乐观锁方式更新编排内容。
//
// 乐观锁是这里的核心：WHERE 里带上调用方读到的 version，只有它仍然匹配才更新。
// 为什么必须这么做——编排文件是多人可编辑的长文本，两个人同时打开详情页、
// 各自改一段再先后保存，如果没有版本校验，后保存的人会静默覆盖先保存的内容，
// 而且双方都不会收到任何提示。这种数据丢失比报错难发现得多。
//
// 返回的三个值：
//   - ComposeStack：更新后的完整记录（带上新的 version，供前端刷新）
//   - bool：是否命中（false 表示 version 不匹配）
//   - error：真正的数据库错误
func (d *DB) UpdateComposeStack(ctx context.Context, agentID, name string, expectedVersion int64, content, configHash string) (ComposeStack, bool, error) {
	// 不读取后回写 env_content：只更新这些列，数据库原子保留当前环境文件。
	q := `UPDATE compose_stacks
	           SET content = ?, config_hash = ?, version = version + 1, updated_at = ?
	           WHERE agent_id = ? AND name = ? AND version = ?
	           RETURNING ` + composeStackColumns
	updated, err := scanComposeStack(d.db.QueryRowContext(ctx, q,
		content, configHash, time.Now().UTC().Unix(), agentID, name, expectedVersion))
	if errors.Is(err, ErrNotFound) {
		// 没命中分两种情况，必须区分：
		//   - 记录不存在 → ErrNotFound（404）
		//   - 记录存在但版本不符 → 乐观锁冲突（409）
		// 两者对前端的处理完全不同：一个是"记录没了"，一个是"重新加载再改"。
		if _, gerr := d.GetComposeStack(ctx, agentID, name); gerr != nil {
			return ComposeStack{}, false, gerr
		}
		return ComposeStack{}, false, nil
	}
	if err != nil {
		return ComposeStack{}, false, fmt.Errorf("更新编排失败: %w", err)
	}
	return updated, true, nil
}

// UpdateComposeProject 原子更新 compose、环境文件、哈希与版本。
// expectedVersion 不匹配时不写入任何字段；at 是本次保存的时间。
// RETURNING 与写入属于同一条语句，返回的记录不会混入后续并发更新。
func (d *DB) UpdateComposeProject(ctx context.Context, agentID, name, content, envContent, configHash string, expectedVersion int64, at time.Time) (ComposeStack, bool, error) {
	q := `UPDATE compose_stacks
		SET content = ?, env_content = ?, config_hash = ?, version = version + 1, updated_at = ?
		WHERE agent_id = ? AND name = ? AND version = ?
		RETURNING ` + composeStackColumns
	updated, err := scanComposeStack(d.db.QueryRowContext(ctx, q,
		content, envContent, configHash, timeToUnix(at), agentID, name, expectedVersion))
	if errors.Is(err, ErrNotFound) {
		// 保留旧更新接口的语义：不存在是 404，版本过期是冲突。
		if _, gerr := d.GetComposeStack(ctx, agentID, name); gerr != nil {
			return ComposeStack{}, false, gerr
		}
		return ComposeStack{}, false, nil
	}
	if err != nil {
		return ComposeStack{}, false, fmt.Errorf("更新编排项目失败: %w", err)
	}
	return updated, true, nil
}

// DeleteComposeStack 删除编排记录。记录不存在返回 ErrNotFound。
func (d *DB) DeleteComposeStack(ctx context.Context, agentID, name string) error {
	res, err := d.db.ExecContext(ctx,
		`DELETE FROM compose_stacks WHERE agent_id = ? AND name = ?`, agentID, name)
	if err != nil {
		return fmt.Errorf("删除编排失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountComposeStacks 统计某台 Agent 上的编排数量（Agent 删除前的提示用）。
func (d *DB) CountComposeStacks(ctx context.Context, agentID string) (int, error) {
	var n int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM compose_stacks WHERE agent_id = ?`, agentID).Scan(&n); err != nil {
		return 0, fmt.Errorf("统计编排数量失败: %w", err)
	}
	return n, nil
}

// MarkComposeStackSynced 记录一次成功的下发。
//
// 单独一个方法而不是并进 UpdateComposeStack，是因为两者的触发时机不同：
// 更新发生在用户保存时，同步发生在 Agent 真的落盘并执行之后。
// 中间可能隔着一次失败重试，如果把 synced_at 写进更新语句里，
// 就会出现"用户保存即认为已同步"的错误结论。
//
// 故意不动 version、content、updated_at：同步只是投递成功，
// 不代表内容发生变化，改动这些字段会让乐观锁误判。
func (d *DB) MarkComposeStackSynced(ctx context.Context, agentID, name string) error {
	res, err := d.db.ExecContext(ctx,
		`UPDATE compose_stacks SET synced_at = ? WHERE agent_id = ? AND name = ?`,
		time.Now().UTC().Unix(), agentID, name)
	if err != nil {
		return fmt.Errorf("记录编排同步时间失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
