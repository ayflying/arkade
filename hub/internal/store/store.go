// Package store 是 Hub 的唯一数据访问层。
//
// 设计原因（决策 D-03）：
//   - 首期用 SQLite 单实例，但业务层不得出现 SQLite 专有语法；
//     建表语句集中在 migrate_sql.go 里按版本顺序执行，方便后续切 PostgreSQL；
//   - SQLite 同一时刻只允许一个写者，连接池必须收缩为 1 个写连接。
//     WAL 模式让读不阻塞写，但写与写之间仍需串行。
package store

import (
	"context"
	"fmt"

	"github.com/gogf/gf/v2/database/gdb"

	"arkade/hub/internal/dbmigrate"
)

// Open 打开并初始化数据库：建连接池、设置 PRAGMA、执行迁移。
func Open(ctx context.Context, path string) (*DB, error) {
	// 连接与连接池参数（WAL / foreign_keys / busy_timeout / synchronous、
	// MaxOpenConns=1、关闭自动时间维护）都在 openGDB 里一次配好——
	// 不留"先建池再改参数"的窗口，那个窗口里建出的连接会带默认参数。
	gdbInst, err := openGDB(path)
	if err != nil {
		return nil, err
	}
	// 从这里开始，任何失败路径都必须关掉 gdbInst，否则库文件句柄会泄漏
	// （Windows 上会直接导致测试目录清理失败）。

	sqlDB, err := masterOf(gdbInst)
	if err != nil {
		// masterOf 内部已负责关闭实例。
		return nil, err
	}

	// 单写连接：SQLite 不允许并发写，设置大于 1 只会在高并发下报错。
	// 这里再设一次是防御性的——真正的设置在 openGDB 的 ConfigNode 里，
	// 但若将来有人绕过 openGDB，这两行能兜住最关键的那条约束。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)

	if err := sqlDB.PingContext(ctx); err != nil {
		closeGDB(gdbInst)
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	db := &DB{gdb: gdbInst}

	// ============================ 迁移由 dbmigrate 负责 ============================
	// 迁移用**独立的短生命周期连接**，不用上面这个业务池。
	// 原因：golang-migrate 的 WithInstance 在 Close 时会关闭传入的 *sql.DB，
	// 若把业务池交给它，迁移一结束业务池就废了
	// （后续所有查询报 "sql: database is closed"）。
	//
	// dbmigrate.Run 内部还会：
	//   - 拿跨进程文件锁（防止多实例同时迁移）；
	//   - 校验 dirty、旧库版本序列、结构漂移，任一不符就拒绝启动。
	if _, err := dbmigrate.Run(ctx, path); err != nil {
		closeGDB(gdbInst)
		return nil, err
	}

	// 预热表元数据缓存，必须在这里做（详见 warmTableMetadata 的说明）。
	if err := warmTableMetadata(ctx, db); err != nil {
		closeGDB(gdbInst)
		return nil, err
	}
	return db, nil
}

// DB 包装数据库连接。所有仓储方法都挂在它上面，
// 避免各 service 各自持有底层连接导致事务边界散乱、连接管理失控。
//
// 只持有 gdb 实例，不保存 *sql.DB：所有读写（含事务）都由 gdb 承担。
// 需要裸连接的场景（如 dbbackup）用 MasterOf 临时取，用完不归还字段。
type DB struct {
	gdb gdb.DB

	// auditPrune 是审计清理的节流状态，必须整体受锁保护，见 audit.go。
	auditPrune auditPruneState

	// lifecycle 保证关闭时先等后台任务结束再关连接，见 lifecycle.go。
	lifecycle lifecycleGate
}

// Close 关闭连接池。幂等。
//
// 顺序：标记关闭 → 等待后台任务 → 关 gdb。
// 反过来的话，后台任务会在已关闭的实例上重新建连接（见 lifecycle 字段的说明）。
func (d *DB) Close() error {
	if d.gdb == nil {
		return nil
	}
	d.lifecycle.shutdown()
	return d.gdb.Close(context.Background())
}

// goBackground 在受生命周期管理的 goroutine 里执行 fn。
//
// 返回 false 表示库已开始关闭，本次任务被拒绝，调用方应放弃。
// fn 的执行有独立超时上限，避免一次慢查询把 Close 无限期拖住。
func (d *DB) goBackground(ctx context.Context, fn func(ctx context.Context)) bool {
	return d.lifecycle.spawn(ctx, fn)
}

// Ping 供健康检查使用。gdb 的 PingMaster 不带 ctx，故这里忽略 ctx。
func (d *DB) Ping(ctx context.Context) error {
	_ = ctx
	return d.gdb.PingMaster()
}

// TxModel 在一个事务里执行 fn，fn 返回错误则自动回滚。
// 跨表写操作都走这里，禁止在业务层手写 Begin/Commit。
//
// 两个约束：
//   - 回调内必须用 tx.Model(...) 而不是 d.gdb.Model(...)：后者会从连接池
//     另取连接，在 MaxOpenConns=1 下直接死锁，在更大池下则是"回滚了但数据还在"；
//   - 回调内不要用 Fields()，原因见 record.go（会触发元数据查询并抢占连接）。
//
// panic 由 gdb 的 callTxFunc 兜住：defer 里 recover 后先 Rollback 再返回错误，
// 不会留下未结束的事务（见 gdb_core_transaction.go）。
//
// 错误原样透传不包装：调用方靠 errors.Is 判断 ErrNotFound/ErrConflict。
// 这是契约选择，不是"包装会破坏 errors.Is"——gerror.Wrap 同样保持 Is 链。
func (d *DB) TxModel(ctx context.Context, fn func(ctx context.Context, tx gdb.TX) error) error {
	return d.gdb.Transaction(ctx, fn)
}
