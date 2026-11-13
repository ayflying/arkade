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
