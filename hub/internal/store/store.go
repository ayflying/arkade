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
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // 纯 Go 实现的 SQLite 驱动，免 CGO，便于交叉编译
)

// Open 打开并初始化数据库：建连接池、设置 PRAGMA、执行迁移。
func Open(ctx context.Context, path string) (*DB, error) {
	// _pragma 参数会应用到池中建立的每一条连接（驱动特性），
	// 而不是只对第一条生效。这点很关键：只设第一条会导致部分连接的
	// journal_mode 回落到默认值，进而在并发下出现 database is locked。
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)",
		path,
	)
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}

	// 单写连接：SQLite 不允许并发写，设置大于 1 只会在高并发下报错。
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	db := &DB{db: sqlDB}
	if err := db.migrate(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// DB 包装 *sql.DB。所有仓储方法都挂在它上面，
// 避免各 service 各自持有 *sql.DB 导致事务边界散乱、连接管理失控。
type DB struct {
	db *sql.DB
}

// Close 关闭连接池。
func (d *DB) Close() error { return d.db.Close() }

// Ping 供健康检查使用。
func (d *DB) Ping(ctx context.Context) error { return d.db.PingContext(ctx) }

// sqlTx 是事务的薄包装，仅用于把 *sql.Tx 传给回调，
// 避免业务层直接依赖 database/sql 的具体类型名而绕过 Tx 的统一管辖。
type sqlTx struct {
	*sql.Tx
}

// Execl 在事务内执行语句。
func (t *sqlTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, query, args...)
}

// Tx 在一个事务里执行 fn，fn 返回错误则自动回滚。
// 所有跨表写操作都必须走这里，禁止在业务层手写 Begin/Commit。
func (d *DB) Tx(ctx context.Context, fn func(tx *sqlTx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启事务失败: %w", err)
	}
	if err := fn(&sqlTx{Tx: tx}); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交事务失败: %w", err)
	}
	return nil
}
