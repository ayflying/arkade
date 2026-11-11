// Package dbmigrate 用 golang-migrate 接管 Hub 的数据库迁移。
//
// ============================ 为什么不用 golang-migrate 的 database/sqlite ============================
// migrate 的 database/sqlite 走 modernc.org/sqlite，它注册 "sqlite"；
// 而 GoFrame 官方驱动也注册 "sqlite"。两者同时 import 会在 **init 阶段** panic：
//
//	panic: sql: Register called twice for driver sqlite
//
// 因此这里用 database/sqlite3（注册名 "sqlite3"，与 "sqlite" 不冲突），
// 通过 WithInstance 把 GoFrame 已建好的 *sql.DB 交给 migrate。
// **绝不调用 sqlite3.Open**——那会用 mattn/go-sqlite3 的 CGO stub，
// 在 CGO_ENABLED=0 下得到一堆"不支持"的错误。
//
// ============================ 为什么迁移用独立连接 ============================
// migrate 的 WithInstance 会**持有并关闭**它拿到的连接（Close 时关）。
// 若直接传业务池的 *sql.DB，迁移结束后业务池就被关了，
// 后续所有查询都会报 "sql: database is closed"。
//
// 因此迁移自己开一个短生命周期连接，用完即关，绝不影响业务池。
package dbmigrate

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/gogf/gf/v2/database/gdb"
	"github.com/golang-migrate/migrate/v4"
	migratesqlite3 "github.com/golang-migrate/migrate/v4/database/sqlite3"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	// GoFrame 官方驱动：唯一注册源。
	_ "github.com/gogf/gf/contrib/drivers/sqlite/v2"
)

// migrationsFS 内嵌全部迁移文件。
//
// 用 embed 而不是运行时读目录：二进制自包含，部署时不会因为
// 忘拷 migrations 目录而启动失败。
//
// ============================ 只有 up，没有 down ============================
// 本项目**不提供 down 迁移**，原因如下：
//
// golang-migrate 对"缺少 down 文件"的处理是：把该版本标记为不可回滚，
// 但 `Down()` / `Steps(-1)` 仍会**成功返回**并推进版本号——也就是说，
// 版本号倒退了、schema 却原样没动。这种"看起来回滚成功"的假象
// 比直接报错危险得多：运维会以为已经回退，继续用旧版本程序跑新结构。
//
// 旧迁移体系只增不改，从未设计过 down 路径。编造 down 会在数据层面
// 产生不可逆破坏（例如删掉后来加入的列，而该列已有业务数据）。
//
// 因此这里**不放 down 文件**。任何回滚必须由人显式决策并手工执行，
// 而不是由程序提供一个会撒谎的按钮。
//
//go:embed migrations/*.up.sql
var migrationsFS embed.FS

// MigrationsTable 是新版本记录表的名字。
//
// ============================ 为什么不沿用 schema_migrations ============================
// 旧表的结构是 (version, name, applied_at)，golang-migrate 的表结构是
// (version, dirty)。两者**语义不同**：
//   - 旧表记录"执行过哪些版本"，没有失败状态；
//   - 新表用单行记录"当前版本 + 是否脏"，是原地推进的游标。
//
// 强行同形会丢失旧表的 name/applied_at（那是历史记录），
// 也会让 golang-migrate 误读旧数据。所以新表独立，旧表**原样保留**。
const MigrationsTable = "arkade_migrate_versions"

// connect 是迁移期间使用的独立连接。
type connect struct {
	gdb gdb.DB
	db  *sql.DB
}

// close 关闭迁移连接。
func (c *connect) close() {
	if c == nil || c.gdb == nil {
		return
	}
	_ = c.gdb.Close(context.Background())
}

// openMigrationConn 建立一个**独立的、短生命周期的**迁移连接。
//
// ============================ 参数必须与业务池一致 ============================
// pragma（WAL / foreign_keys / busy_timeout）必须与业务连接一致，否则：
//   - foreign_keys 未开时，迁移里的外键语句不会校验，脏数据能写进去；
//   - journal_mode 不一致会让迁移与业务连接争抢锁的方式不同。
//
// 但连接池不同：迁移只需要一条连接，且用完即关。
func openMigrationConn(path string) (*connect, error) {
	node := gdb.ConfigNode{
		Type:  "sqlite",
		Name:  path,
		Extra: "journal_mode=WAL&foreign_keys=1&busy_timeout=5000&synchronous=NORMAL",

		// 迁移全程串行，一条连接足够。
		//
		// ============================ 这里有个必须注意的死锁陷阱 ============================
		// MaxOpen=1 意味着池里只有一条连接：
		// 若在遍历 rows 未关闭的情况下**再发一个查询**，
		// 第二个查询会等待唯一那条连接，而第一条的 rows 还在占着它 —— 永久死锁。
		// 所以本包内所有"查完再查"的地方都必须先 rows.Close()。
		MaxOpenConnCount: 1,
		MaxIdleConnCount: 1,

		TimeMaintainDisabled: true,
	}
	gdbInst, err := gdb.New(node)
	if err != nil {
		return nil, fmt.Errorf("建立迁移连接失败: %w", err)
	}
	sqlDB, err := gdbInst.Master()
	if err != nil {
		_ = gdbInst.Close(context.Background())
		return nil, fmt.Errorf("取得迁移连接失败: %w", err)
	}
	return &connect{gdb: gdbInst, db: sqlDB}, nil
}

// newMigrator 基于给定连接构造 migrate 实例。
//
// 注意：返回的 *migrate.Migrate 在 Close() 时会关闭传入的 driver，
// 而 sqlite3 的 driver.Close() 会关闭底层 *sql.DB。
// 因此这里传入的必须是**迁移专用连接**，绝不是业务池。
//
// ============================ 这是个写入动作 ============================
// 构造 migrator 会创建 arkade_migrate_versions 表（若不存在）。
// 因此凡是"可能被拒绝的库"，都必须在**调用本函数之前**完成全部校验，
// 否则拒绝时已经留下了新表，等于污染现场。
// 调用方 runOn 已按此顺序安排。
func newMigrator(conn *connect) (*migrate.Migrate, error) {
	driver, err := migratesqlite3.WithInstance(conn.db, &migratesqlite3.Config{
		MigrationsTable: MigrationsTable,
	})
	if err != nil {
		return nil, fmt.Errorf("构造迁移驱动失败: %w", err)
	}

	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("打开内嵌迁移源失败: %w", err)
	}

	// ============================ 包一层 down 守卫 ============================
	// 本项目不提供 down 脚本，而 golang-migrate 在缺少 down 时会
	// "执行空脚本 + 推进版本号 + 返回成功"，造成已回滚的假象。
	// 这里在 source 层把所有 ReadDown 变成明确错误。
	//
	// 这是唯一能覆盖全部回滚入口的位置：Down / Steps(负数) / Migrate(降级)
	// 最终都走 ReadDown，而 Steps 是无法从外部包装的具体方法。
	src = newDownGuardSource(src)

	m, err := migrate.NewWithInstance("iofs", src, "sqlite3", driver)
	if err != nil {
		return nil, fmt.Errorf("构造迁移实例失败: %w", err)
	}
	return m, nil
}

// embeddedVersions 返回内嵌迁移文件的版本号（升序、去重）。
//
// 用于和数据库里的已应用版本比对，判断"是否有未知版本"。
func embeddedVersions() ([]int, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("读取内嵌迁移目录失败: %w", err)
	}
	seen := make(map[int]struct{})
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		// 文件名形如 000001_legacy_v1.up.sql，取前缀数字。
		idx := strings.IndexByte(name, '_')
		if idx <= 0 {
			continue
		}
		v := 0
		if _, err := fmt.Sscanf(name[:idx], "%d", &v); err != nil {
			return nil, fmt.Errorf("迁移文件名 %q 的版本号无法解析: %w", name, err)
		}
		seen[v] = struct{}{}
	}
	out := make([]int, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Ints(out)
	return out, nil
}
