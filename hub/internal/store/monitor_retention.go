package store

import (
	"arkade/hub/internal/dao"
	"context"
	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"
)

// PruneMonitorHistory 清理截止时间之前的已结束事件和投递审计。
// before 是 Unix 秒；非正值不执行删除。任何未结束事件都不受保留期影响，
// 即使它的 last_seen 很早也必须留下，供引擎重启恢复。
func (d *DB) PruneMonitorHistory(ctx context.Context, before int64) error {
	if before <= 0 {
		return nil
	}
	return d.TxModel(ctx, func(ctx context.Context, tx gdb.TX) error {
		dc := dao.MonitorDeliveries.Columns()
		if _, err := tx.Model(dao.MonitorDeliveries.Table()).Ctx(ctx).WhereLT(dc.CreatedAt, before).Delete(); err != nil {
			return gerror.Wrap(err, "清理监控投递历史失败")
		}
		ic := dao.MonitorIncidents.Columns()
		_, err := tx.Model(dao.MonitorIncidents.Table()).Ctx(ctx).WhereIn(ic.Status, []string{"resolved", "closed"}).WhereLT(ic.LastSeen, before).Delete()
		return gerror.Wrap(err, "清理已结束监控事件失败")
	})
}
