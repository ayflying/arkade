package store

import "context"

// 顺序与列表一致，秒级时间相同按ID固定排序，保证每次保留同一批最新记录。
// 保留原有90天过期策略：即最多9999条，并非保证保留满9999条。
const auditCountPruneSQL = `DELETE FROM audit_logs WHERE id IN (
 SELECT id FROM audit_logs ORDER BY created_at DESC, id DESC LIMIT 9223372036854775807 OFFSET $1
)`

// PruneAuditCount在启动时压缩旧库，避免没有新操作时历史记录仍超上限。
func (d *DB) PruneAuditCount(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, auditCountPruneSQL, AuditMaxRecords)
	return err
}
