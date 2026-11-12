// Package store is the Hub PostgreSQL data access layer.
package store

import (
	"context"
	"database/sql"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/gogf/gf/v2/errors/gerror"

	"arkade/hub/internal/dbmigrate"
)

// Open opens a PostgreSQL DSN, migrates and warms ORM metadata. No file paths.
func Open(ctx context.Context, dsn string) (*DB, error) {
	inst, err := openGDB(dsn)
	if err != nil {
		return nil, err
	}
	raw, err := masterOf(inst)
	if err != nil {
		return nil, err
	}
	if err = raw.PingContext(ctx); err != nil {
		closeGDB(inst)
		return nil, gerror.New("连接 PostgreSQL 失败（请检查数据库配置和服务状态）")
	}
	if _, err = dbmigrate.Run(ctx, dsn); err != nil {
		closeGDB(inst)
		return nil, err
	}
	db := &DB{gdb: inst}
	if err = warmTableMetadata(ctx, db); err != nil {
		closeGDB(inst)
		return nil, err
	}
	return db, nil
}

// DB owns the ORM pool and background lifecycle.
type DB struct {
	gdb        gdb.DB
	auditPrune auditPruneState
	lifecycle  lifecycleGate
}

// Close waits for background work before releasing the pool. Idempotent.
func (d *DB) Close() error {
	if d.gdb == nil {
		return nil
	}
	d.lifecycle.shutdown()
	return d.gdb.Close(context.Background())
}
func (d *DB) goBackground(ctx context.Context, fn func(context.Context)) bool {
	return d.lifecycle.spawn(ctx, fn)
}
func (d *DB) Ping(ctx context.Context) error {
	raw, err := d.gdb.Master()
	if err != nil {
		return err
	}
	return raw.PingContext(ctx)
}

// TxModel 在多连接、多个 Open 实例乃至多个 Hub 进程之间保护多行不变量。
// 最后管理员/最后登录方式、规范化同名服务器、编排幂等键与容量等均含先查后写，
// 因此采用数据库级 advisory 事务锁，而不是只在单进程有效的 Go mutex。
// 锁随事务提交/回滚（含 panic）自动释放，回调必须使用 tx.Model 保持事务边界。
// 显式 READ COMMITTED 保证等待锁之后各查询获得新快照，不能改成可重复读旧快照。
// 锁键有意与迁移器相同：启动迁移与业务事务互斥，避免迁移时读取半成品结构。
func (d *DB) TxModel(ctx context.Context, fn func(context.Context, gdb.TX) error) error {
	opts := gdb.DefaultTxOptions()
	opts.Isolation = sql.LevelReadCommitted
	return d.gdb.TransactionWithOptions(ctx, opts, func(ctx context.Context, tx gdb.TX) error {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(1095912257, 1)"); err != nil {
			return gerror.Wrap(err, "获取仓储事务锁失败")
		}
		return fn(ctx, tx)
	})
}
