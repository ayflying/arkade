package store

// metadata.go 在打开库时预热 gdb 的表元数据缓存。
//
// ============================ 为什么必须预热 ============================
// gdb 的 Model 在多种操作上会去取表结构：
//   - Fields(...) 会调 filterFieldsFrom → HasTable / TableFields 做列名校验；
//   - 用 DO（struct）Insert/Update 时要按结构体标签映射列，同样会取表结构。
//
// 这些取结构的动作走的是**连接池**，不是当前事务连接。
// 本库 MaxOpenConns=1（SQLite 单写约束），一旦事务已占住唯一连接，
// 事务内再去取表结构就会无限等待那条不会归还的连接——
// 表现为首次 CRUD **永久挂起**，而不是报错。
//
// gdb 把表结构缓存在 Core 的 innerMemCache 里且不设过期（NoExpire）。
// 本库建表只在迁移时发生，运行期没有动态 DDL，所以**在 Open 里预热一次**
// 就能保证此后所有事务内的取结构都命中缓存、不碰连接池。
//
// 这就是把预热放在 Open（此时没有任何事务在跑）而不是放在事务里的原因。

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
