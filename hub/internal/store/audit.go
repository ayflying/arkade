package store

// audit.go 是操作审计日志的仓储层。
//
// 设计要点：
//   - 写入是旁路操作，永远不应该让业务失败（调用方忽略错误）；
//   - 查询支持按时间倒序分页与多条件筛选；
//   - 过期清理在写入时按时间间隔触发，不做全表扫描。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"time"

	"arkade/hub/internal/audit"
)

// 审计保留与清理相关常量集中在此，便于统一调整。
const (
	// AuditRetentionDays 是审计记录保留天数。
	//
	// 90 天是运维复盘与合规审计的常见口径：足够覆盖一个季度的排查窗口，
	// 又不至于让表无限膨胀。
	AuditRetentionDays = 90

	// AuditPruneInterval 是两次清理尝试之间的最小间隔。
	//
	// 为什么不在每次写入时都跑一次 DELETE：
	// 清理要扫 created_at 索引并删除大量行，开销随数据量增长。
	// 审计写入是每次有副作用请求都会触发的热路径，
	// 每次都清理会让"写一条日志"变成"可能删几万行"，
	// 直接把接口延迟拖垮。按时间间隔触发把成本摊平到可忽略。
	AuditPruneInterval = time.Hour
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
// 本方法内部完成两件事：插入记录、按间隔触发过期清理。
// 清理失败不影响插入结果——清理是维护动作，不该让一条本该记下的
// 审计因为"删旧数据失败"而丢失。
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

	_, err := d.db.ExecContext(ctx,
		`INSERT INTO audit_logs
			(id, user_id, username, agent_id, agent_name, action, target, detail,
			 result, error, duration_ms, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		id, rec.UserID, rec.Username, rec.AgentID, rec.AgentName, rec.Action,
		rec.Target, rec.Detail, rec.Result, rec.Error, rec.DurationMs,
		created.Unix(),
	)
	if err != nil {
		return fmt.Errorf("写入审计记录: %w", err)
	}

	// 顺手清理过期记录（按时间间隔节流，见常量注释）。
	// 单独起 goroutine：删除可能耗时，不能拖慢这次请求的响应。
	// 即使清理失败也只是日志问题，返回给调用方的仍是"插入成功"。
	d.maybePruneAudit()
	return nil
}

// maybePruneAudit 在满足间隔条件时异步清理过期审计。
func (d *DB) maybePruneAudit() {
	now := time.Now()

	d.auditPrune.mu.Lock()
	// 用"上次尝试时间"而不是"上次成功时间"判断间隔：
	// 若清理一直失败（比如磁盘满），用成功时间会导致每次写入都重试，
	// 反而把故障放大成性能问题。
	if now.Sub(d.auditPrune.lastPrune) < AuditPruneInterval {
		d.auditPrune.mu.Unlock()
		return
	}
	d.auditPrune.lastPrune = now
	d.auditPrune.mu.Unlock()

	go func() {
		cutoff := now.AddDate(0, 0, -AuditRetentionDays).Unix()
		// 清理失败只在下次写入时再试，不向任何人报错：
		// 业务方对审计清理的失败无能为力，弹错误只会制造噪音。
		_, _ = d.db.ExecContext(context.Background(),
			`DELETE FROM audit_logs WHERE created_at < $1`, cutoff)
	}()
}

// ListAuditLogs 按筛选条件分页查询审计，返回记录与总数。
//
// 返回 total 是为了前端能画分页器：只给当前页数据的话，
// 前端不知道后面还有多少页，只能靠"这一页不满就到底了"去猜，
// 遇到 limit 恰好整除总条数时永远多出一次空刷新。
func (d *DB) ListAuditLogs(ctx context.Context, f AuditFilters) ([]audit.Record, int, error) {
	where, args := buildAuditWhere(f)

	// 先查总数。
	var total int
	if err := d.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_logs`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计审计记录: %w", err)
	}

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

	rows, err := d.db.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询审计记录: %w", err)
	}
	defer rows.Close()

	out := make([]audit.Record, 0, limit)
	for rows.Next() {
		var r audit.Record
		var created int64
		if err := rows.Scan(&r.ID, &r.UserID, &r.Username, &r.AgentID, &r.AgentName,
			&r.Action, &r.Target, &r.Detail, &r.Result, &r.Error, &r.DurationMs, &created); err != nil {
			return nil, 0, fmt.Errorf("扫描审计记录: %w", err)
		}
		r.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历审计记录: %w", err)
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

// PruneAuditLogs 立即清理过期审计，返回删除行数。
//
// 暴露成独立方法主要是为了测试：正常路径走的是 WriteAudit 里的
// 节流清理，测试没法等一小时。
func (d *DB) PruneAuditLogs(ctx context.Context) (int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -AuditRetentionDays).Unix()
	res, err := d.db.ExecContext(ctx,
		`DELETE FROM audit_logs WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("清理过期审计: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CountAuditLogs 返回审计总条数（不过滤）。
func (d *DB) CountAuditLogs(ctx context.Context) (int, error) {
	var n int
	err := d.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs`).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
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

// auditPruneState 是清理节流用的状态，挂在 DB 上。
//
// 定义成独立结构体而不是 DB 的两个字段，是为了让"这两个字段必须
// 一起被锁保护"这件事在类型上就成立，减少后来人只改一半的概率。
type auditPruneState struct {
	mu        sync.Mutex
	lastPrune time.Time
}
