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
	"errors"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"arkade/hub/internal/model/entity"
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
	_, err := d.gdb.Exec(ctx, q,
		s.ID, s.AgentID, s.Name, s.Content, s.EnvContent, s.ConfigHash, s.Version,
		timeToUnix(s.CreatedAt), timeToUnix(s.UpdatedAt), timeToUnix(s.SyncedAt),
	)
	return normalizeExecError(err, "创建编排")
}

// composeStackFields 返回查询列名。
func composeStackFields() []string {
	c := dao.ComposeStacks.Columns()
	return []string{c.Id, c.AgentId, c.Name, c.Content, c.EnvContent,
		c.ConfigHash, c.Version, c.CreatedAt, c.UpdatedAt, c.SyncedAt}
}

// composeStackFromEntity 把 entity 行转成 ComposeStack（常规查询路径）。
func composeStackFromEntity(e entity.ComposeStacks) ComposeStack {
	return ComposeStack{
		ID:         e.Id,
		AgentID:    e.AgentId,
		Name:       e.Name,
		Content:    e.Content,
		EnvContent: e.EnvContent,
		ConfigHash: e.ConfigHash,
		Version:    int64(e.Version),
		CreatedAt:  unixToTime(int64(e.CreatedAt)),
		UpdatedAt:  unixToTime(int64(e.UpdatedAt)),
		SyncedAt:   unixToTime(int64(e.SyncedAt)),
	}
}

// composeStackFromRecord 用于 RETURNING 路径：gdb.GetOne 返回 Record，
// 用它的 Struct 转到 entity 再走同一套转换，避免第二份手写映射。
func composeStackFromRecord(rec gdb.Record) (ComposeStack, error) {
	var e entity.ComposeStacks
	if err := rec.Struct(&e); err != nil {
		return ComposeStack{}, gerror.Wrap(err, "解析编排记录失败")
	}
	return composeStackFromEntity(e), nil
}

// GetComposeStack 按 (agentID, name) 查编排。
//
// 用「Agent + 项目名」而不是主键 id 作为查询条件：对外所有接口的路径都是
// /agents/{agentID}/stacks/{name}，项目名才是用户视角的标识；id 只在内部用。
func (d *DB) GetComposeStack(ctx context.Context, agentID, name string) (ComposeStack, error) {
	c := dao.ComposeStacks.Columns()
	var e entity.ComposeStacks
	err := scanOne(ctx, d.gdb.Model(dao.ComposeStacks.Table()).Fields(composeStackFields()).
		Where(c.AgentId, agentID).
		Where(c.Name, name), &e)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ComposeStack{}, ErrNotFound
		}
		return ComposeStack{}, gerror.Wrap(err, "查询编排失败")
	}
	return composeStackFromEntity(e), nil
}

// ListComposeStacks 列出某台 Agent 的全部编排，按项目名升序（界面顺序稳定）。
//
// agentID 为空时返回空列表而不是报错：调用方只会在已知 Agent 的上下文里调用，
// 空值属于编程错误，但没必要为此让接口 500。
func (d *DB) ListComposeStacks(ctx context.Context, agentID string) ([]ComposeStack, error) {
	c := dao.ComposeStacks.Columns()
	var ents []entity.ComposeStacks
	if err := scanAll(ctx, d.gdb.Model(dao.ComposeStacks.Table()).Fields(composeStackFields()).
		Where(c.AgentId, agentID).
		Order(c.Name+" ASC"), &ents); err != nil {
		return nil, gerror.Wrap(err, "查询编排列表失败")
	}

	out := make([]ComposeStack, 0, len(ents))
	for _, e := range ents {
		out = append(out, composeStackFromEntity(e))
	}
	return out, nil
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
	updated, err := d.composeStackViaReturning(ctx, q,
		content, configHash, time.Now().UTC().Unix(), agentID, name, expectedVersion)
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
		return ComposeStack{}, false, gerror.Wrap(err, "更新编排失败")
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
	updated, err := d.composeStackViaReturning(ctx, q,
		content, envContent, configHash, timeToUnix(at), agentID, name, expectedVersion)
	if errors.Is(err, ErrNotFound) {
		// 保留旧更新接口的语义：不存在是 404，版本过期是冲突。
		if _, gerr := d.GetComposeStack(ctx, agentID, name); gerr != nil {
			return ComposeStack{}, false, gerr
		}
		return ComposeStack{}, false, nil
	}
	if err != nil {
		return ComposeStack{}, false, gerror.Wrap(err, "更新编排项目失败")
	}
	return updated, true, nil
}

// DeleteComposeStack 删除编排记录。记录不存在返回 ErrNotFound。
func (d *DB) DeleteComposeStack(ctx context.Context, agentID, name string) error {
	c := dao.ComposeStacks.Columns()
	res, err := d.gdb.Model(dao.ComposeStacks.Table()).Ctx(ctx).
		Where(c.AgentId, agentID).
		Where(c.Name, name).
		Delete()
	if err != nil {
		return gerror.Wrap(err, "删除编排失败")
	}
	return requireAffected(res, "删除编排")
}

// CountComposeStacks 统计某台 Agent 上的编排数量（Agent 删除前的提示用）。
func (d *DB) CountComposeStacks(ctx context.Context, agentID string) (int, error) {
	n, err := d.gdb.Model(dao.ComposeStacks.Table()).Ctx(ctx).
		Where(dao.ComposeStacks.Columns().AgentId, agentID).
		Count()
	if err != nil {
		return 0, gerror.Wrap(err, "统计编排数量失败")
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
	c := dao.ComposeStacks.Columns()
	res, err := d.gdb.Model(dao.ComposeStacks.Table()).Ctx(ctx).
		Where(c.AgentId, agentID).
		Where(c.Name, name).
		Data(do.ComposeStacks{SyncedAt: time.Now().UTC().Unix()}).
		Update()
	if err != nil {
		return gerror.Wrap(err, "记录编排同步时间失败")
	}
	return requireAffected(res, "记录编排同步时间")
}

// composeStackViaReturning 执行带 RETURNING 的乐观锁 UPDATE 并解析返回行。
//
// 为什么这里保留原生 SQL：UPDATE ... SET version = version + 1 ...
// WHERE version = ? RETURNING <列> 是**一条**语句——版本匹配、自增、读回
// 三者原子完成。用 Model 链式 API 无法表达 RETURNING，拆成
// "先 Update 再 Select" 就会在两条语句之间插进并发更新，
// 返回的记录可能已被别人改过，前端拿到的 version 随即失效。
//
// 并发安全来自 WHERE version = ? 与 RowsAffected 的原子匹配，
// 不在 RETURNING 本身；没有返回行即表示版本不匹配或记录不存在。
// 调用方据 ErrNotFound 区分这两种情况。
func (d *DB) composeStackViaReturning(ctx context.Context, query string, args ...any) (ComposeStack, error) {
	one, err := d.gdb.GetOne(ctx, query, args...)
	if err != nil {
		return ComposeStack{}, gerror.Wrap(err, "查询编排失败")
	}
	if len(one) == 0 {
		return ComposeStack{}, ErrNotFound
	}
	return composeStackFromRecord(one)
}
