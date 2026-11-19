package store

// auto_adopt.go 是"外部编排自动发现"扫描状态的仓储层。
//
// ============================ 它解决的是什么 ============================
//
// 自动发现需要对**尚未纳管**的外部项目读取编排配置，而读配置在 Agent 侧
// 意味着创建一个一次性 helper 容器。因此"多久读一次"直接决定容器创建频率。
//
// 改造前：状态只存在 Hub 进程内存里，且成功以外的结果一律不落库，
// 于是"读不到 configuration"的项目**每 30 秒被重读一次**，永不停止。
// 现场表现就是容器列表里堆积几十个 arkade-helper-*，并持续写入磁盘。
//
// 改造后这里落两类"不要再重扫"的记录：
//   - skipped：确定性阻塞（多配置文件、危险工作目录、符号链接等），
//     同样的输入必然同样失败，只有用户手动重试或目标真的变了才值得再读；
//   - failed：环境性失败（引擎连不上、缺镜像、超时），按指数退避重试，
//     超过上限后同样转为不再自动重扫。
//
// 成功接管的项目**不写这张表**：那由 compose_stacks.synced_at 表达，
// 两处各记一份会让"谁才算已接管"出现两个互相矛盾的答案。

import (
	"context"
	"time"

	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/entity"
)

// 扫描状态的两种取值。停止条件是"不要再自动扫"，与"用户看到什么"分开表达。
const (
	// AutoAdoptStatusSkipped 表示确定性阻塞：不再自动重扫。
	AutoAdoptStatusSkipped = "skipped"
	// AutoAdoptStatusFailed 表示环境性失败：按退避重试，超过上限后不再自动重扫。
	AutoAdoptStatusFailed = "failed"
)

// AutoAdoptState 是一行扫描状态。
type AutoAdoptState struct {
	Status        string
	Reason        string
	Attempts      int64
	NextCheckAt   time.Time // 零值表示不再自动重扫
	LastScannedAt time.Time
}

// SaveAutoAdoptState 写入（或覆盖）一条扫描状态。
//
// 用 upsert 而不是"先查后写"：扫描是后台并发循环，两条语句之间的窗口
// 会让同一个项目落成两行（虽有主键兜底，但错误信息会变成 constraint 冲突，
// 掩盖真正的失败原因）。
func (d *DB) SaveAutoAdoptState(ctx context.Context, agent, name string, s AutoAdoptState) error {
	_, err := d.gdb.Exec(ctx,
		`INSERT INTO auto_adopt_states(agent_id, name, status, reason, attempts, next_check_at, last_scanned_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(agent_id, name) DO UPDATE SET
		   status = excluded.status, reason = excluded.reason, attempts = excluded.attempts,
		   next_check_at = excluded.next_check_at, last_scanned_at = excluded.last_scanned_at`,
		agent, name, s.Status, s.Reason, s.Attempts, timeToUnix(s.NextCheckAt), timeToUnix(s.LastScannedAt),
	)
	return normalizeExecError(err, "保存自动发现扫描状态")
}

// GetAutoAdoptState 读取一条扫描状态，不存在时返回 ErrNotFound。
func (d *DB) GetAutoAdoptState(ctx context.Context, agent, name string) (AutoAdoptState, error) {
	c := dao.AutoAdoptStates.Columns()
	var e entity.AutoAdoptStates
	err := scanOne(ctx, d.gdb.Model(dao.AutoAdoptStates.Table()).Fields(c.AgentId, c.Name, c.Status, c.Reason, c.Attempts, c.NextCheckAt, c.LastScannedAt).
		Where(c.AgentId, agent).
		Where(c.Name, name), &e)
	if err != nil {
		if err == ErrNotFound {
			return AutoAdoptState{}, ErrNotFound
		}
		return AutoAdoptState{}, gerror.Wrap(err, "查询自动发现扫描状态失败")
	}
	return AutoAdoptState{
		Status:        e.Status,
		Reason:        e.Reason,
		Attempts:      e.Attempts,
		NextCheckAt:   unixToTime(e.NextCheckAt),
		LastScannedAt: unixToTime(e.LastScannedAt),
	}, nil
}

// ListAutoAdoptStates 一次取出某台 Agent 的全部扫描状态。
//
// 后台扫描每轮都要按项目判断"该不该再扫"，逐个 Get 会产生 N 次查询；
// 一次取回后在内存里查表，轮次开销与项目数无关。
func (d *DB) ListAutoAdoptStates(ctx context.Context, agentID string) (map[string]AutoAdoptState, error) {
	c := dao.AutoAdoptStates.Columns()
	var rows []entity.AutoAdoptStates
	err := d.gdb.Model(dao.AutoAdoptStates.Table()).
		Fields(c.AgentId, c.Name, c.Status, c.Reason, c.Attempts, c.NextCheckAt, c.LastScannedAt).
		Where(c.AgentId, agentID).
		Scan(&rows)
	if err != nil {
		return nil, gerror.Wrap(err, "查询自动发现扫描状态失败")
	}
	out := make(map[string]AutoAdoptState, len(rows))
	for _, e := range rows {
		out[e.Name] = AutoAdoptState{
			Status:        e.Status,
			Reason:        e.Reason,
			Attempts:      e.Attempts,
			NextCheckAt:   unixToTime(e.NextCheckAt),
			LastScannedAt: unixToTime(e.LastScannedAt),
		}
	}
	return out, nil
}

// DeleteAutoAdoptState 删除一条扫描状态（目标已经不需要再跳过时使用）。
//
// 典型场景：用户手动点了"重试"，或项目已经被纳管成功，
// 此时旧结论必须清掉，否则下次扫描会继续按老结论跳过。
func (d *DB) DeleteAutoAdoptState(ctx context.Context, agent, name string) error {
	c := dao.AutoAdoptStates.Columns()
	_, err := d.gdb.Model(dao.AutoAdoptStates.Table()).Ctx(ctx).
		Where(c.AgentId, agent).
		Where(c.Name, name).
		Delete()
	if err != nil {
		return gerror.Wrap(err, "删除自动发现扫描状态失败")
	}
	return nil
}

// DeleteAutoAdoptStatesExcept 删除该 Agent 下**不在 keep 里**的扫描状态。
//
// 为什么需要：外部项目被用户删掉之后，它的扫描状态会一直留着。
// 不清理的话，项目名被重新创建时会带着老结论（例如"多配置文件"）被跳过，
// 而现场其实已经换成了另一个项目。每轮扫描结束时按当前项目集对齐一次即可。
func (d *DB) DeleteAutoAdoptStatesExcept(ctx context.Context, agentID string, keep []string) error {
	c := dao.AutoAdoptStates.Columns()
	m := d.gdb.Model(dao.AutoAdoptStates.Table()).Ctx(ctx).Where(c.AgentId, agentID)
	if len(keep) > 0 {
		m = m.WhereNotIn(c.Name, keep)
	}
	if _, err := m.Delete(); err != nil {
		return gerror.Wrap(err, "清理失效的自动发现扫描状态失败")
	}
	return nil
}
