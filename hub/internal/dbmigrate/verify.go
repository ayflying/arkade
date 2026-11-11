package dbmigrate

// verify.go：结构漂移检测。
//
// ============================ 这个方法在做什么 ============================
// 判断"库里的表结构，是否与迁移序列在该版本应有的结构一致"。
//
// 做法：在内存里（不落盘）用迁移脚本从零重建一份期望结构，
// 然后与真实库逐表、逐索引、逐触发器比对。
//
// 为什么这样比：库可能经历过手工修表、版本升级、拷贝恢复。
// 它的版本号说"我到 v10 了"，但实际结构可能是 v8 的样子，或者被人加过一列。
// 只信版本号是不够的——**结构才是事实**。
//
// ============================ 排除哪些对象 ============================
// 只比业务结构，排除：
//   - schema_migrations（旧体系记录表）
//   - arkade_migrate_versions（新体系游标表）
//   - sqlite_ 前缀（引擎内部表与自动索引）
//
// 排除按 **tbl_name 归属**判断，不是按名字前缀——因为具名约束生成的索引
// （例如 schema_migrations 的 version_unique）不带 sqlite_ 前缀。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	// 内存库也走同一驱动，保证 DDL 解释一致。
	_ "github.com/gogf/gf/contrib/drivers/sqlite/v2"

	gdbdriver "github.com/gogf/gf/v2/database/gdb"
)

// verifySchemaAt 用迁移序列重建到 upto 版本的期望结构，与真实库比对。
//
// 返回 nil 表示一致；否则返回包含具体差异的错误。
func verifySchemaAt(ctx context.Context, actual *sql.DB, upto int) error {
	if upto <= 0 {
		return nil
	}

	want, err := buildExpectedSchema(ctx, upto)
	if err != nil {
		return err
	}

	got, err := readSchema(ctx, actual)
	if err != nil {
		return err
	}

	diffs := diffSchema(want, got, fmt.Sprintf("按迁移序列重建的 v%d 库", upto), "实际库")
	if len(diffs) == 0 {
		return nil
	}

	const maxShow = 10
	var b strings.Builder
	fmt.Fprintf(&b, "库结构与迁移序列 v%d 不一致（发现 %d 处差异），拒绝启动：\n", upto, len(diffs))
	for i, d := range diffs {
		if i >= maxShow {
			fmt.Fprintf(&b, "  ...还有 %d 处未显示\n", len(diffs)-maxShow)
			break
		}
		fmt.Fprintf(&b, "  - %s\n", d)
	}
	b.WriteString("这可能是因为该库被手工改过结构，或由不同版本的迁移建立。\n")
	b.WriteString("程序不会自动修复：请人工确认差异后再决定如何处理。")
	return fmt.Errorf("%s", b.String())
}

// verifyLegacySchema 是旧的入口名（保留给接管场景调用）。
func verifyLegacySchema(ctx context.Context, actual *sql.DB, applied []legacyApplied) error {
	if len(applied) == 0 {
		return nil
	}
	return verifySchemaAt(ctx, actual, applied[len(applied)-1].version)
}

// buildExpectedSchema 在内存库中按迁移序列重建到 upto 版本，返回其 schema。
//
// ============================ 为什么整脚本一次执行 ============================
// 驱动支持多语句 Exec（已实测）。整脚本一次喂进去有两个好处：
//  1. 不需要自造 SQL 切分器（那种解析器必然漏掉字符串/触发器里的分号）；
//  2. 重建路径与 golang-migrate 执行真实迁移的路径**完全一致**，
//     避免"比对用的结构"与"迁移出来的结构"因为执行方式不同而产生假差异。
func buildExpectedSchema(ctx context.Context, upto int) ([]schemaObject, error) {
	// 内存库必须限制为单连接：每个 :memory: 连接都是**独立的空库**，
	// 池里若有多条连接，建表和读 schema 可能落在不同库上，结果永远为空。
	node := gdbdriver.ConfigNode{
		Type:                 "sqlite",
		Name:                 ":memory:",
		Extra:                "journal_mode=MEMORY&foreign_keys=1",
		MaxOpenConnCount:     1,
		MaxIdleConnCount:     1,
		TimeMaintainDisabled: true,
	}
	gdbInst, err := gdbdriver.New(node)
	if err != nil {
		return nil, fmt.Errorf("建立内存库失败: %w", err)
	}
	defer func() { _ = gdbInst.Close(context.Background()) }()

	db, err := gdbInst.Master()
	if err != nil {
		return nil, fmt.Errorf("取得内存库连接失败: %w", err)
	}

	files, err := legacyUpFiles(upto)
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		// 整脚本一次执行，与迁移路径一致。
		if _, err := db.ExecContext(ctx, f.body); err != nil {
			// 这里失败说明**代码里的迁移序列自身有问题**（不是库的问题）。
			// 必须明确区分，否则会误导排查方向。
			return nil, fmt.Errorf(
				"迁移序列自检失败：v%d 在空库上执行报错: %w", f.version, err)
		}
	}

	return readSchema(ctx, db)
}
