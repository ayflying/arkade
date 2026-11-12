package store

import (
	"arkade/hub/internal/dao"
	"arkade/hub/internal/model/do"
	"context"
	"time"

	"github.com/gogf/gf/v2/errors/gerror"
)

// PruneAuditBefore deletes strictly older rows; exact-boundary rows are retained.
func (d *DB) PruneAuditBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	result, err := d.gdb.Model(dao.AuditLogs.Table()).Ctx(ctx).WhereLT("created_at", cutoff.Unix()).Data(do.AuditLogs{}).Delete()
	if err != nil {
		return 0, gerror.Wrap(err, "清理过期审计")
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, gerror.Wrap(err, "读取审计清理数量")
	}
	return n, nil
}

// 顺序与列表一致，秒级时间相同按ID固定排序，保证每次保留同一批最新记录。
// 保留原有90天过期策略：即最多9999条，并非保证保留满9999条。
//
// 占位符用 ? 而不是 $1：gdb 的 Exec 走 database/sql 的驱动占位符，
// 两种写法在 SQLite 驱动下都能用，但统一成 ? 可以避免同一常量在
// 不同调用点被两种风格混用而出错。
const auditCountPruneSQL = `DELETE FROM audit_logs WHERE id IN (
 SELECT id FROM audit_logs ORDER BY created_at DESC, id DESC LIMIT 9223372036854775807 OFFSET ?
)`

// PruneAuditCount 在启动时压缩旧库，避免没有新操作时历史记录仍超上限。
func (d *DB) PruneAuditCount(ctx context.Context) error {
	if _, err := d.gdb.Exec(ctx, auditCountPruneSQL, AuditMaxRecords); err != nil {
		return gerror.Wrap(err, "压缩审计记录失败")
	}
	return nil
}
