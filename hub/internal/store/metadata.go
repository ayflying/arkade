package store

// ORM field metadata is queried through the pool, not the current transaction.
// Warm immutable schema metadata before requests start to avoid pool starvation
// under concurrent transactions and to validate generated DO bindings early.

import (
	"context"

	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dao"
)

// businessTables 返回全部业务表名。新增表时必须加进来，否则该表首次
// 事务内 CRUD 会重新出现上面说的挂起。
//
// 用 DAO 的表名而不是手写字符串：生成器改了表名，这里跟着改，不会漂移。
func businessTables() []string {
	return []string{
		dao.Users.Table(),
		dao.Agents.Table(),
		dao.AgentTokens.Table(),
		dao.AuditLogs.Table(),
		dao.ComposeStacks.Table(),
		dao.ComposeOperationRequests.Table(),
		dao.ExternalComposeDrafts.Table(),
		dao.Groups.Table(),
		dao.GroupAgents.Table(),
		dao.Sessions.Table(),
		dao.UserGroups.Table(),
		dao.SystemSettings.Table(),
		dao.OidcProviders.Table(),
		dao.UserIdentities.Table(),
	}
}

// warmTableMetadata 预热全部业务表的字段元数据。
//
// 由 Open 在建库/迁移完成后、任何业务查询之前调用，此时没有事务占用连接。
func warmTableMetadata(ctx context.Context, d *DB) error {
	// 先整表清单入缓存，再逐表取字段：TableFields 内部也会校验表存在，
	// 两步都做掉才不会留下"清单有、字段没缓"的空档。
	if _, err := d.gdb.GetCore().GetTablesWithCache(); err != nil {
		return gerror.Wrap(err, "预热表清单失败")
	}
	for _, table := range businessTables() {
		if _, err := d.gdb.TableFields(ctx, table); err != nil {
			return gerror.Wrapf(err, "预热表 %s 字段失败", table)
		}
	}
	return nil
}
