package store

// driver.go：SQLite 驱动注册与 gdb 实例构造。
//
// ============================ 唯一约束 ============================
// 本项目里 **sqlite 驱动只能有一个注册源**。
//
// GoFrame 官方驱动注册 "sqlite"；modernc.org/sqlite 的顶层包也注册 "sqlite"。
// 两者同时出现会在 **init 阶段** panic：
//
//	panic: sql: Register called twice for driver sqlite
//
// 该 panic 早于 main()、无法 recover、编译期看不出来。
// 因此禁止在任何文件里 import modernc.org/sqlite 的顶层包
// （modernc.org/sqlite/lib 作为间接底层依赖存在是正常的，
// 它是 glebarez/go-sqlite 的实现，但**不注册驱动**）。

import (
	"context"
	"database/sql"
	"fmt"

	// 唯一注册源。同时向 database/sql 注册 "sqlite"，
	// 并向 gdb 注册驱动类型（gdb.New 按 Type 查找）。
	_ "github.com/gogf/gf/contrib/drivers/sqlite/v2"

	"github.com/gogf/gf/v2/database/gdb"
)

// driverName 是 database/sql 里的驱动名，固定为 "sqlite"
// （官方驱动 sqlite_open.go 内部写死 sql.Open("sqlite", source)）。
const driverName = "sqlite"

// pragmaExtra 是官方驱动接受的 pragma 配置，格式 key=value&key=value。
//
// 注意：官方驱动**不接受** database/sql 风格的 DSN 查询参数，
// 按 "path?_pragma=..." 写会报 "invalid link configuration"。
// 它只认 ConfigNode 的 Name(路径) + Extra(pragma)。
//
// PRAGMA 理由：
//   - journal_mode(WAL)：读不阻塞写；
//   - foreign_keys(1)：SQLite 默认不强制外键，必须每连接开启；
//   - busy_timeout(5000)：拿不到写锁时等待而非立刻失败；
//   - synchronous(NORMAL)：WAL 下推荐档位（FULL 每次提交 fsync，OFF 断电可能丢事务）。
const pragmaExtra = "journal_mode=WAL&foreign_keys=1&busy_timeout=5000&synchronous=NORMAL"

// openGDB 建立一个独立的 gdb 实例并返回它。
//
// ============================ 为什么返回 gdb 实例而不是 *sql.DB ============================
// 后续 DAO 迁移需要 gdb 实例（生成 DAO 的方法要绑定到具体实例，
// 多库测试才不会串库）。*sql.DB 只是它的 Master()，两者必须**同源同生命周期**，
// 否则关掉 *sql.DB 之后 gdb 实例仍持有已关闭的池。
//
// 连接池参数在这里（open 阶段）就设好，不留"Open 之后再改"的窗口——
// 那个窗口里若有连接被建出来，就会带着默认池参数。
func openGDB(path string) (gdb.DB, error) {
	node := gdb.ConfigNode{
		Type:  "sqlite",
		Name:  path,
		Extra: pragmaExtra,

		// 单连接是本项目的**简化并发策略**，不是 SQLite 的硬性要求：
		// WAL 下读与写可并发，多连接也能工作，只是写与写之间仍需串行，
		// 需要自己处理 busy/locked 重试。这里取 1 换取行为可预期。
		// 字段名是 MaxOpenConnCount/MaxIdleConnCount（不是 MaxOpenConns）。
		MaxOpenConnCount: 1,
		MaxIdleConnCount: 1,

		// 关闭 gdb 的自动时间维护：本库的时间列是 INTEGER Unix 秒，
		// 由业务代码显式写入（timeToUnix），与 gdb 自动维护的 datetime 字符串不同格式。
		// 两者混用会让同一列出现两种表示，排序与比较失真。
		TimeMaintainDisabled: true,
	}
	gdbInst, err := gdb.New(node)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	return gdbInst, nil
}

// masterOf 取 gdb 实例的底层 *sql.DB，失败时释放实例。
//
// 单独抽出来是因为"取 Master 失败"是一个容易漏掉的资源泄漏点：
// gdb.New 已经建好实例与连接池，取不到 Master 时必须关掉它，
// 否则临时目录里的库文件句柄会一直占着（Windows 上尤其明显，
// 会导致 t.TempDir() 清理失败）。
func masterOf(gdbInst gdb.DB) (*sql.DB, error) {
	sqlDB, err := gdbInst.Master()
	if err != nil {
		closeGDB(gdbInst)
		return nil, fmt.Errorf("取得数据库连接失败: %w", err)
	}
	if sqlDB == nil {
		closeGDB(gdbInst)
		return nil, fmt.Errorf("打开数据库失败: 未取得有效连接")
	}
	return sqlDB, nil
}

// closeGDB 关闭 gdb 实例。
//
// gdb.DB 只提供 Close(ctx)，且它关的就是底层的 *sql.DB。
// 用 context.Background 而不是调用方的 ctx：关闭动作不应该
// 因为业务请求被取消而半途而废（那会泄漏连接）。
func closeGDB(gdbInst gdb.DB) {
	if gdbInst == nil {
		return
	}
	_ = gdbInst.Close(context.Background())
}
