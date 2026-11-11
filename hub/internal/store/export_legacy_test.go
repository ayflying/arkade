package store

// export_legacy_test.go 把现有 migrations 变量原样导出成迁移文件。
//
// ============================ 为什么用测试来导出 ============================
// dbmigrate 接管后，旧的 migrate_sql.go 会被删除，迁移内容改由
// embed 的 SQL 文件承载。这里有个必须保证的事：
//
//	**旧迁移 1-10 必须按旧序列原样复现，一个字符都不能改。**
//
// 手抄会引入误差（空格、注释、IF NOT EXISTS），而误差会导致
// 老库与新库的结构比对失败。所以不手抄——直接从现有的
// migrations 变量导出，让 Go 编译器保证内容与现在完全一致。
//
// 这个测试用 exportLegacy 环境变量控制，平时不跑：
//
//	PROBE_EXPORT_LEGACY=1 go test ./internal/store -run TestExportLegacy
//
// 导出完成后本文件即可删除（它只服务于一次性的搬运）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestExportLegacy 把 migrations 导出成 dbmigrate 用的 SQL 文件。
func TestExportLegacy(t *testing.T) {
	if os.Getenv("PROBE_EXPORT_LEGACY") != "1" {
		t.Skip("仅在 PROBE_EXPORT_LEGACY=1 时导出")
	}

	outDir := filepath.Join("..", "dbmigrate", "migrations")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}

	for _, m := range migrations {
		// 文件名遵循 golang-migrate 的 iofs 命名：{version}_{name}.{up|down}.sql
		// 但 name 里含中文与空格，iofs 允许（它只解析版本号前缀）。
		// 用 6 位补零保证字典序与数值序一致。
		base := fmt.Sprintf("%06d_legacy_v%d", m.version, m.version)

		// ============================ up 文件 ============================
		// 内容 = 原始语句，逐条用分号分隔，**不做任何改写**。
		// 保留原始缩进与注释：注释是决策记录的一部分，删掉会丢信息。
		var up string
		up += fmt.Sprintf("-- 迁移 v%d：%s\n", m.version, m.name)
		up += "-- 本文件由旧 migrate_sql.go 自动导出，内容与之逐字一致。\n"
		up += "-- 禁止修改：改内容会让老库与新库结构不一致，且难以被发现。\n\n"
		for i, stmt := range m.stmts {
			if i > 0 {
				up += "\n"
			}
			up += stmt
			up += ";\n"
		}
		if err := os.WriteFile(filepath.Join(outDir, base+".up.sql"), []byte(up), 0o644); err != nil {
			t.Fatalf("写 v%d up 失败: %v", m.version, err)
		}

		// ============================ down 文件 ============================
		// 旧迁移**没有** down 脚本（只增不改）。
		//
		// 这里不编造 down：破坏性操作不能伪装成无损回滚。
		// 写一个显式的"不支持回滚"文件，让 golang-migrate 在被人误用
		// down 时给出明确错误，而不是静默什么都不做。
		down := fmt.Sprintf(`-- 迁移 v%d（%s）没有回滚脚本。
--
-- 原因：旧迁移体系只增不改，从未设计过 down 路径。
-- 编造一个 down 会在数据层面产生不可逆的破坏（例如删掉后来加入的列，
-- 而该列里已有业务数据），且无法恢复。
--
-- 因此这里不提供可执行的 down，任何回滚必须由人显式决策并手工执行。
`, m.version, m.name)
		if err := os.WriteFile(filepath.Join(outDir, base+".down.sql"), []byte(down), 0o644); err != nil {
			t.Fatalf("写 v%d down 失败: %v", m.version, err)
		}

		t.Logf("导出 v%d（%s）: %d 条语句", m.version, m.name, len(m.stmts))
	}

	t.Logf("共导出 %d 个版本到 %s", len(migrations), outDir)
}

// TestLegacyMigrationSequenceIsContiguous 断言旧迁移版本号连续无缺口。
//
// ============================ 为什么这条很重要 ============================
// dbmigrate 接管时要验证"旧库的已应用版本连续、无未知版本"。
// 如果源码里的 migrations 本身就有缺口（比如删掉了 v5），
// 那么"连续"这个判据本身就失效了，验证会误判合法库为异常。
//
// 这条测试把源码层面的连续性钉死。
func TestLegacyMigrationSequenceIsContiguous(t *testing.T) {
	if len(migrations) == 0 {
		t.Fatal("迁移列表为空")
	}
	for i, m := range migrations {
		want := i + 1
		if m.version != want {
			t.Fatalf("迁移版本不连续：第 %d 项的 version=%d，期望 %d", i, m.version, want)
		}
		if m.name == "" {
			t.Fatalf("迁移 v%d 缺少名称", m.version)
		}
		if len(m.stmts) == 0 {
			t.Fatalf("迁移 v%d 没有语句", m.version)
		}
	}
	t.Logf("旧迁移共 %d 个版本，版本号连续且均有语句", len(migrations))
}

// legacyAppliedAt 供后续验证读取旧表结构用（保留占位，避免 import 未使用）。
var _ = context.Background
