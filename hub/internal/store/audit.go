package store

// audit.go 是操作审计日志的仓储层。
//
// 设计要点：
//   - 写入是旁路操作，永远不应该让业务失败（调用方忽略错误）；
//   - 查询支持按时间倒序分页与多条件筛选；
//   - 过期清理在写入时按时间间隔触发，不做全表扫描。

import (
	"context"
	"strings"
	"time"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/audit"
	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
)

// 审计保留与清理相关常量集中在此，便于统一调整。
const (
	// AuditRetentionDays 是审计记录保留天数的**上限，也是默认值**。
	//
	// 30 天是产品设定：审计只用于近期复盘与追责，不承担长期归档。
	// 它是硬上限而不是默认值——设置项只能在 1..30 之间调，超出即拒绝。
	// 需要保存更久的实例应当在 Arkade 之外归档查询结果，而不是让这张表
	// 无限增长：审计写入是每次有副作用请求都会触发的热路径，表越大，
	// 过期清理的 DELETE 越重，最终拖慢的是业务接口本身。
	AuditRetentionDays = 30

	// AuditMinRetentionDays 是允许的最小保留期。
	// 保留 0 天等于"写完立刻删"，审计就失去意义，所以下限是 1 天。
	AuditMinRetentionDays = 1
)

// AuditFilters 是审计列表的筛选条件。零值表示不过滤。
type AuditFilters struct {
	UserID  string
	AgentID string
	Action  string
	Result  string
	// From / To 是闭区间的 Unix 秒。零值表示该侧不限。
	From int64
	To   int64
	// Limit 为 0 时用 AuditDefaultLimit。
	Limit  int
	Offset int
}

// 分页边界。上限必须有：审计表可能有几十万行，
// 不设上限时一个 limit=999999 就能让 Hub 内存暴涨。
const (
	AuditDefaultLimit = 50
	AuditMaxLimit     = 200
)

// WriteAudit 写入一条审计记录。
//
// 实现了 audit.Writer 接口。
//
// 过期清理**只由实例级保留 worker 负责**（每小时按设置里的天数删除），
// 写入路径不做任何清理：审计写入是每个有副作用请求都会走的热路径，
// 在这里顺手删旧记录等于让"写一条日志"变成"可能删几万行"，
// 一旦表变大就会把接口延迟拖垮。
func (d *DB) WriteAudit(rec audit.Record) error {
	ctx := context.Background()

	// ID 为空时补一个：调用方漏填不应该导致整条记录写不进去。
	// 审计的价值在于"记录下来"，字段不全但记下了，远好过整条丢失。
	id := rec.ID
	if id == "" {
		id = audit.NewID()
	}
	created := rec.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}

	err := d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		if _, err := tx.Model(dao.AuditLogs.Table()).Ctx(ctx).Data(do.AuditLogs{
			Id:         id,
			UserId:     rec.UserID,
			Username:   rec.Username,
			AgentId:    rec.AgentID,
			AgentName:  rec.AgentName,
			Action:     rec.Action,
			Target:     rec.Target,
			Detail:     rec.Detail,
			Result:     rec.Result,
			Error:      rec.Error,
			DurationMs: rec.DurationMs,
			CreatedAt:  created.Unix(),
		}).Insert(); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return gerror.Wrap(err, "写入审计记录")
	}

	// Time retention belongs exclusively to the instance retention worker.
	return nil
}

// ListAuditLogs 按筛选条件分页查询审计，返回记录与总数。
//
// 返回 total 是为了前端能画分页器：只给当前页数据的话，
// 前端不知道后面还有多少页，只能靠"这一页不满就到底了"去猜，
// 遇到 limit 恰好整除总条数时永远多出一次空刷新。
func (d *DB) ListAuditLogs(ctx context.Context, f AuditFilters) ([]audit.Record, int, error) {
	where, args := buildAuditWhere(f)

	// 先查总数。
	one, err := d.gdb.GetOne(ctx, `SELECT COUNT(*) AS n FROM audit_logs`+where, args...)
	if err != nil {
		return nil, 0, gerror.Wrap(err, "统计审计记录")
	}
	total := one["n"].Int()

	limit := f.Limit
	if limit <= 0 {
		limit = AuditDefaultLimit
	}
	if limit > AuditMaxLimit {
		limit = AuditMaxLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	// 排序列上再按 id 兜底：created_at 是秒级精度，
	// 同一秒内的多条记录若只按时间排，顺序不稳定，
	// 翻页时可能重复或漏掉某条。
	query := `SELECT id, user_id, username, agent_id, agent_name, action, target,
				detail, result, error, duration_ms, created_at
			  FROM audit_logs` + where +
		` ORDER BY created_at DESC, id DESC LIMIT $` + itoa(len(args)+1) +
		` OFFSET $` + itoa(len(args)+2)

	recs, err := d.gdb.GetAll(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, gerror.Wrap(err, "查询审计记录")
	}

	out := make([]audit.Record, 0, len(recs))
	for _, r := range recs {
		var rec audit.Record
		rec.ID = r["id"].String()
		rec.UserID = r["user_id"].String()
		rec.Username = r["username"].String()
		rec.AgentID = r["agent_id"].String()
		rec.AgentName = r["agent_name"].String()
		rec.Action = r["action"].String()
		rec.Target = r["target"].String()
		rec.Detail = r["detail"].String()
		rec.Result = r["result"].String()
		rec.Error = r["error"].String()
		rec.DurationMs = r["duration_ms"].Int64()
		rec.CreatedAt = time.Unix(r["created_at"].Int64(), 0).UTC()
		out = append(out, rec)
	}
	return out, total, nil
}

// buildAuditWhere 拼装 WHERE 子句与参数。
//
// 参数占位符用 $N 递推而不是把值拼进 SQL：
// 筛选值来自查询参数（用户名、动作标识等），直接拼接就是注入。
func buildAuditWhere(f AuditFilters) (string, []any) {
	var conds []string
	var args []any
	add := func(col, val string) {
		if val == "" {
			return
		}
		args = append(args, val)
		conds = append(conds, col+" = $"+itoa(len(args)))
	}

	add("user_id", f.UserID)
	add("agent_id", f.AgentID)
	add("action", f.Action)
	add("result", f.Result)

	if f.From > 0 {
		args = append(args, f.From)
		conds = append(conds, "created_at >= $"+itoa(len(args)))
	}
	if f.To > 0 {
		args = append(args, f.To)
		conds = append(conds, "created_at <= $"+itoa(len(args)))
	}

	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// 审计保留期的**唯一实现**是 PruneAuditBefore(ctx, cutoff)。
//
// 不要再加"按固定天数清理"的等价方法：保留期由系统设置决定，任何写死
// 天数的 DELETE 都会与可配置策略打架（用户改成 7 天后，仍有一条固定
// 30 天的清理在跑，行为就无法解释）。历史实现曾有两个入口——
// 一个是写入路径的节流清理，一个是 worker 按设置清理——两者并存时
// 谁也说不清实际保留了多少天，已统一为只剩 worker 一条。

// CountAuditLogs 返回审计总条数（不过滤）。
func (d *DB) CountAuditLogs(ctx context.Context) (int, error) {
	n, err := d.gdb.Model(dao.AuditLogs.Table()).Ctx(ctx).Count()
	if err != nil {
		return 0, gerror.Wrap(err, "统计审计总数失败")
	}
	return n, nil
}

// itoa 是 strconv.Itoa 的本地别名。
//
// 包一层只为了避免在多个 SQL 拼装处反复写 import，
// 同时让占位符拼接在代码里一眼可辨（看到 itoa 就知道在拼 SQL 序号）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// 审计写入是热路径，不做任何清理；过期删除只由 auditretention worker
// 按系统设置的天数执行（见 service/auditretention）。
