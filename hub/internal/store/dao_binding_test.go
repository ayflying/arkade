package store

// dao_binding_test.go：验证生成 DAO 的元数据可以绑定到独立 gdb 实例。
//
// ============================ 这个测试要证明什么 ============================
// GoFrame 生成的 DAO 有个**无法绕过的特性**：
//
//	func (dao *UsersDao) Ctx(ctx) *gdb.Model {
//	    model := dao.DB().Model(dao.table)   // ← DB() 是 g.DB("default")，全局默认实例
//	    for _, h := range dao.handlers { model = h(model) }
//	    return model.Safe().Ctx(ctx)
//	}
//
// 也就是说：**调用 Ctx() 一定会先碰全局默认 DB**，handlers 只是后置修饰。
// 想让 DAO 走一个独立的 gdb 实例，靠 Handler 是做不到的。
//
// 我们的需求恰恰是"多库隔离"——测试要能同时开多个独立的 PostgreSQL 库，
// 各自的 store.DB 互不干扰。若用全局默认 DB，两个测试库会打架。
//
// ============================ 采用的方案 ============================
// 用生成的 DAO 提供**元数据**（表名、列名、DO/Entity 结构），
// 但查询落在我们自己持有的 gdb 实例上：
//
//	s.gdb.Model(dao.Users.Table())...
//
// 好处：
//   - 表名/列名来自生成物，改表结构后重新生成即可，不会写错；
//   - DO/Entity 直接用生成的结构体，字段类型与 JSON tag 保持一致；
//   - 不碰生成物内部，也不需要伪造一个全局默认库。
//
// 本章测试钉住这个方案的两个前提：
//   1. `dao.X.Table()` / `Columns()` 是纯值，调用不触发全局 DB；
//   2. 同一个 DAO 的 Table() 可以安全地用在**任意** gdb 实例上，
//      两个不同实例之间互不干扰。

import (
	"arkade/hub/internal/testdb"
	"context"
	"fmt"
	"testing"

	"arkade/hub/internal/dao"

	"github.com/gogf/gf/v2/database/gdb"
)

// TestDaoMetadataIsSideEffectFree 调用 DAO 元数据不应触发全局 DB。
//
// ============================ 为什么要测这个 ============================
// 若 `dao.Users.Table()` 内部其实走了 `g.DB()`，那么在**没有配置数据库**的
// 环境里（例如生产启动早期）调用它就会 panic。
// 这是"用元数据而非 Ctx"这一方案能否成立的前提。
func TestDaoMetadataIsSideEffectFree(t *testing.T) {
	// 注意：这里不初始化任何数据库配置。
	// 若 Table()/Columns() 内部依赖全局 DB，本测试会 panic 或报错。
	table := dao.Users.Table()
	if table != "users" {
		t.Fatalf("表名应为 users，实际 %q", table)
	}

	cols := dao.Users.Columns()
	if cols.Id != "id" || cols.Username != "username" {
		t.Fatalf("列名元数据异常: %+v", cols)
	}
	if cols.CreatedAt != "created_at" {
		t.Fatalf("created_at 列名异常: %q", cols.CreatedAt)
	}
}

// TestSameDaoTableBindsToIndependentInstances 同一 DAO 可绑定到不同实例。
//
// ============================ 这条证明多库隔离 ============================
// 两个独立的 PostgreSQL 数据库、两个独立的 store.DB，用**同一个** DAO 的表名元数据，
// 分别写入不同数据，互相不可见。
//
// 这正是测试需要的能力：每个测试用 testdb.New(t) 创建的独立库，
// 不应该因为共享全局默认 DB 而互相污染。
func TestSameDaoTableBindsToIndependentInstances(t *testing.T) {
	ctx := context.Background()

	// 两个完全独立的库。
	dbA, err := Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatalf("打开库 A 失败: %v", err)
	}
	defer func() { _ = dbA.Close() }()

	dbB, err := Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatalf("打开库 B 失败: %v", err)
	}
	defer func() { _ = dbB.Close() }()

	// 用同一个 DAO 的元数据，往两个库分别写入。
	table := dao.Users.Table()
	cols := dao.Users.Columns()

	insert := func(db *DB, id, name string) {
		t.Helper()
		_, err := db.gdb.Exec(ctx, fmt.Sprintf(
			`INSERT INTO %s(%s, %s, %s, %s, %s, %s, %s)
			 VALUES(?,?,?,?,?,EXTRACT(EPOCH FROM CURRENT_TIMESTAMP)::BIGINT,EXTRACT(EPOCH FROM CURRENT_TIMESTAMP)::BIGINT)`,
			table, cols.Id, cols.Username, cols.DisplayName, cols.PasswordHash,
			cols.Role, cols.CreatedAt, cols.UpdatedAt),
			id, name, name, "h", "admin")
		if err != nil {
			t.Fatalf("写入 %s 到 %s 失败: %v", id, name, err)
		}
	}
	insert(dbA, "u1", "alice")
	insert(dbB, "u2", "bob")

	// 各自只能看到自己的数据。
	count := func(db *DB) int {
		t.Helper()
		v, err := db.gdb.Model(table).Ctx(ctx).Count()
		if err != nil {
			t.Fatalf("统计失败: %v", err)
		}
		return v
	}
	if got := count(dbA); got != 1 {
		t.Errorf("库 A 应有 1 行，实际 %d", got)
	}
	if got := count(dbB); got != 1 {
		t.Errorf("库 B 应有 1 行，实际 %d", got)
	}

	// 关键断言：A 里看不到 B 的用户。
	v, err := dbA.gdb.Model(table).Ctx(ctx).Where(cols.Username, "bob").Count()
	if err != nil {
		t.Fatalf("跨库查询失败: %v", err)
	}
	if v != 0 {
		t.Fatal("库 A 看到了库 B 的数据——多库隔离失效")
	}
}

// TestDaoTransactionUsesSameInstance 事务必须落在同一个实例上。
//
// ============================ 为什么单独测 ============================
// "用 store.DB.Model(dao.Table())" 这个方案要能支持事务：
// 事务内的所有操作必须与开启事务的连接一致。
// 若 Model 走的是另一个 gdb 实例，事务就成了摆设。
func TestDaoTransactionUsesSameInstance(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatalf("打开库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	table := dao.Users.Table()
	cols := dao.Users.Columns()

	// 事务内写入后回滚，数据不应留下。
	wantErr := fmt.Errorf("故意回滚")
	err = db.gdb.Transaction(ctx, func(ctx context.Context, tx gdb.TX) error {
		_, ierr := tx.Exec(fmt.Sprintf(
			`INSERT INTO %s(%s, %s, %s, %s, %s, %s, %s)
			 VALUES(?,?,?,?,?,EXTRACT(EPOCH FROM CURRENT_TIMESTAMP)::BIGINT,EXTRACT(EPOCH FROM CURRENT_TIMESTAMP)::BIGINT)`,
			table, cols.Id, cols.Username, cols.DisplayName, cols.PasswordHash,
			cols.Role, cols.CreatedAt, cols.UpdatedAt),
			"u1", "alice", "Alice", "h", "admin")
		if ierr != nil {
			return ierr
		}
		return wantErr
	})
	if err != wantErr {
		t.Fatalf("期望回滚错误，实际 %v", err)
	}

	v, err := db.gdb.Model(table).Ctx(ctx).Count()
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if v != 0 {
		t.Fatal("事务回滚后数据仍存在——事务与查询不在同一实例")
	}
}
