package store

// driver_test.go 验证驱动层的两个关键不变量。
//
// ============================ 这里测什么、为什么 ============================
//
//  1. **sqlite 驱动只有一个注册源**。若将来有人重新 import 了
//     modernc.org/sqlite 的顶层包，启动会直接 panic（init 阶段、
//     无法 recover）。这条测试把那种回归提前到测试期暴露。
//
//  2. **实际使用的 SQLite 版本支持现有 SQL**。Hub 现有迁移与查询
//     大量使用 unixepoch()，它需要 SQLite >= 3.38.0。
//     GoFrame 官方驱动的底层版本会随依赖图变化（MVS 可能提升
//     modernc 的版本），因此必须实测而不是假设。

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSQLiteDriverRegistered 断言 "sqlite" 驱动可用。
//
// ============================ 这条测试的**能力边界**（不要夸大） ============================
// sql.Register 本身就会拒绝重复注册（panic），所以 sql.Drivers() 里
// 永远不可能出现两个同名项。因此本测试**无法**"验证只有一个注册源"——
// 那种冲突会让整个包在 init 阶段就崩，测试根本跑不起来。
//
// 它实际能证明的只有一件事：**当前确实存在可用的 sqlite 驱动**，
// 且名字就是 driverName。真正的重复注册防护是 CI 规则
// （禁止顶层 import modernc.org/sqlite 与 migrate database/sqlite），
// 以及 openGDB 能成功建实例这一点。
func TestSQLiteDriverRegistered(t *testing.T) {
	drivers := sql.Drivers()
	found := false
	for _, d := range drivers {
		if d == driverName {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("期望 %q 驱动已注册，实际注册表: %v", driverName, drivers)
	}
	t.Logf("已注册驱动: %v", drivers)

	// 更强的证据：真的能通过它建出一个实例并可查询。
	gdbInst, err := openGDB(filepath.Join(t.TempDir(), "reg.db"))
	if err != nil {
		t.Fatalf("通过 %q 建实例失败: %v", driverName, err)
	}
	defer closeGDB(gdbInst)

	sqlDB, err := gdbInst.Master()
	if err != nil {
		t.Fatalf("取底层连接失败: %v", err)
	}
	var one int
	if err := sqlDB.QueryRowContext(context.Background(), "select 1").Scan(&one); err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if one != 1 {
		t.Fatalf("select 1 返回 %d", one)
	}
}

// TestSQLiteVersionSupportsUnixepoch 实测底层 SQLite 版本与 unixepoch()。
//
// 在 hub 模块里，modernc 会因 MVS 被提升到比 GoFrame 驱动声明更高的版本
// （hub 直接依赖 v1.34.4，而 GF 驱动只要求 >= v1.23.1）。
// 版本提升本身通常无害，但"底层 SQLite 特性集变了"这件事必须实测确认。
func TestSQLiteVersionSupportsUnixepoch(t *testing.T) {
	ctx := context.Background()
	gdbInst, err := openGDB(filepath.Join(t.TempDir(), "ver.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer closeGDB(gdbInst)

	sqlDB, err := gdbInst.Master()
	if err != nil {
		t.Fatalf("取底层连接失败: %v", err)
	}

	var ver string
	if err := sqlDB.QueryRowContext(ctx, "select sqlite_version()").Scan(&ver); err != nil {
		t.Fatalf("查询 SQLite 版本失败: %v", err)
	}
	t.Logf("底层 SQLite 版本 = %s（GOOS=%s CGO 构建=%s）", ver, runtime.GOOS, cgoState())

	// 版本解析：unixepoch() 需要 >= 3.38.0。
	var major, minor, patch int
	if _, err := fmt.Sscanf(ver, "%d.%d.%d", &major, &minor, &patch); err != nil {
		t.Fatalf("无法解析版本号 %q: %v", ver, err)
	}
	if major < 3 || (major == 3 && minor < 38) {
		t.Fatalf("SQLite %s 低于 3.38.0，unixepoch() 不可用（现有 SQL 会全部失败）", ver)
	}

	// 实跑，不只看版本号：某些构建可能裁掉了函数。
	var epoch int64
	if err := sqlDB.QueryRowContext(ctx, "select unixepoch()").Scan(&epoch); err != nil {
		t.Fatalf("unixepoch() 不可用: %v", err)
	}
	if epoch <= 0 {
		t.Fatalf("unixepoch() 返回异常值 %d", epoch)
	}
	t.Logf("unixepoch() 实跑成功 = %d", epoch)

	// 复刻现有迁移/查询里最常见的两种写法。
	if _, err := sqlDB.ExecContext(ctx, `create table t_probe (
		id integer primary key autoincrement,
		created_at integer not null
	)`); err != nil {
		t.Fatalf("建表失败: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `insert into t_probe (created_at) values (unixepoch())`); err != nil {
		t.Fatalf("insert ... unixepoch() 失败: %v", err)
	}
	var n int
	if err := sqlDB.QueryRowContext(ctx,
		`select count(*) from t_probe where created_at >= unixepoch() - 3600`).Scan(&n); err != nil {
		t.Fatalf("where ... unixepoch() 失败: %v", err)
	}
	if n != 1 {
		t.Fatalf("时间窗查询应为 1 行，实际 %d", n)
	}
}

// TestPragmaApplied 验证 pragma 真的下发到了连接上。
//
// ============================ 为什么不能只看配置 ============================
// pragma 是**连接级**状态。配置里写了不代表驱动真的下发了——
// 尤其换驱动后（modernc → glebarez），拼接规则完全变了。
// 若 foreign_keys 没生效，外键约束会**静默失效**：
// 插入指向不存在父行的数据不会报错，等发现时数据已经脏了。
func TestPragmaApplied(t *testing.T) {
	ctx := context.Background()
	gdbInst, err := openGDB(filepath.Join(t.TempDir(), "pragma.db"))
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	defer closeGDB(gdbInst)

	sqlDB, err := gdbInst.Master()
	if err != nil {
		t.Fatalf("取底层连接失败: %v", err)
	}

	cases := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
	}
	for _, c := range cases {
		var got string
		if err := sqlDB.QueryRowContext(ctx, "PRAGMA "+c.pragma).Scan(&got); err != nil {
			t.Fatalf("读取 %s 失败: %v", c.pragma, err)
		}
		if !strings.EqualFold(got, c.want) {
			t.Errorf("PRAGMA %s = %q，期望 %q", c.pragma, got, c.want)
		}
	}

	// 外键的实际效果：插入不存在的父行必须被拒绝。
	if _, err := sqlDB.ExecContext(ctx, `create table fk_parent (id integer primary key)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `create table fk_child (
		id integer primary key,
		parent_id integer not null references fk_parent(id)
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `insert into fk_child (id, parent_id) values (1, 999)`); err == nil {
		t.Fatal("外键未生效：插入了不存在的父行")
	} else {
		t.Logf("外键约束生效: %v", err)
	}
}

// cgoState 报告当前二进制的 CGO 状态，用于把测试结果与构建方式对应起来。
func cgoState() string {
	// 运行时无法直接读 CGO_ENABLED，用是否链接 cgo 的常见判据代替：
	// 纯 Go 构建下 runtime 只报 GOOS/GOARCH。这里直接标注构建参数来源，
	// 真实值以 go env / 构建命令为准。
	return "见构建命令 (CGO_ENABLED=0)"
}
