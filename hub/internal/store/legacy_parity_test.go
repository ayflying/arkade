package store

// legacy_parity_test.go：旧迁移变量与内嵌 up 脚本的一致性验证。
//
// ============================ 这个测试防什么 ============================
// 内嵌的 migrations/*.up.sql 是从旧的 `migrations` 变量**程序化导出**的。
// 导出这一步本身可能出错：漏掉一条语句、改了一个字符、缩进被重排。
//
// 更危险的是：如果导出错了，配套的测试仍然会全绿——
// 因为那些测试拿"导出结果"当预期值，等于自己验证自己。
//
// 因此这里做**同源对照**：
//
//	左边：git 里的旧 `migrations` 变量（独立于导出过程的原始事实）
//	右边：内嵌的 .up.sql 文件
//
// 两边各自在**独立的空库**里执行，然后逐表、逐索引、逐触发器比对
// 实际产生的 schema。这正是"导出是否忠实"的等价测试。
//
// ============================ 为什么不比对文本 ============================
// 直接比字符串会把"注释格式/空行"也算进去，而导出时加了说明性头注释。
// 该保证的是**语义等价**：执行后产出同样的结构。
// 所以这里比 sqlite_master。

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/gogf/gf/v2/database/gdb"
	"sort"
	"strings"
	"testing"

	"arkade/hub/internal/dbmigrate"
)

// TestLegacyMigrationsParityWithEmbedded 验证旧变量与内嵌脚本产出相同结构。
func TestLegacyMigrationsParityWithEmbedded(t *testing.T) {
	for _, v := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			// 左边：用旧 `migrations` 变量的语句建库。
			fromVar, err := applyLegacyVarUpTo(t, v)
			if err != nil {
				t.Fatalf("按旧变量建 v%d 失败: %v", v, err)
			}

			// 右边：用内嵌 .up.sql 文件建库。
			fromEmbed, err := applyEmbeddedUpTo(t, v)
			if err != nil {
				t.Fatalf("按内嵌脚本建 v%d 失败: %v", v, err)
			}

			// 逐对象比对。
			if err := compareSchemas(fromVar, fromEmbed, v); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// applyLegacyVarUpTo 用旧 `migrations` 变量建一个到 upto 版本的内存库。
//
// 关键：**逐条 stmt 执行**，保持旧代码的原始语义与顺序，
// 不经过任何"导出/重排"环节。
func applyLegacyVarUpTo(t *testing.T, upto int) ([]schemaRow, error) {
	t.Helper()
	db := newMemoryDB(t)

	for _, m := range migrations {
		if m.version > upto {
			continue
		}
		for i, stmt := range m.stmts {
			if _, err := db.ExecContext(context.Background(), stmt); err != nil {
				return nil, fmt.Errorf("v%d 第 %d 条失败: %w", m.version, i+1, err)
			}
		}
	}
	return dumpSchema(t, db)
}

// applyEmbeddedUpTo 用内嵌 .up.sql 文件建一个到 upto 版本的内存库。
func applyEmbeddedUpTo(t *testing.T, upto int) ([]schemaRow, error) {
	t.Helper()
	db := newMemoryDB(t)

	entries, err := embeddedMigrationNames()
	if err != nil {
		return nil, err
	}

	for _, name := range entries {
		var version int
		if _, err := fmt.Sscanf(name, "%d", &version); err != nil {
			return nil, fmt.Errorf("解析版本号失败 %q: %w", name, err)
		}
		if version > upto {
			continue
		}
		body, err := dbmigrate.ReadMigration(name)
		if err != nil {
			return nil, err
		}
		if _, err := db.ExecContext(context.Background(), body); err != nil {
			return nil, fmt.Errorf("%s 执行失败: %w", name, err)
		}
	}
	return dumpSchema(t, db)
}

// embeddedMigrationNames 返回内嵌的 up 脚本文件名（按版本排序）。
func embeddedMigrationNames() ([]string, error) {
	return dbmigrate.ListUpScriptNames()
}

// schemaRow 是 sqlite_master 的一行。
type schemaRow struct {
	kind   string
	name   string
	tble   string
	sqlLen int
	sql    string
}

// dumpSchema 读出全部 schema 对象（排除 SQLite 内部对象）。
//
// 这里**不排除** schema_migrations/arkade_migrate_versions：
// 两边都是纯建表脚本、都没建这两张表，排除与否结果一样，
// 但不排除能顺带发现"某一侧意外建了版本表"的错误。
func dumpSchema(t *testing.T, db *sql.DB) ([]schemaRow, error) {
	t.Helper()
	rows, err := db.QueryContext(context.Background(),
		`SELECT type, name, COALESCE(tbl_name,''), COALESCE(sql,'')
		 FROM sqlite_master
		 WHERE name NOT LIKE 'sqlite_%'
		 ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("读 schema 失败: %w", err)
	}
	defer rows.Close()

	var out []schemaRow
	for rows.Next() {
		var r schemaRow
		if err := rows.Scan(&r.kind, &r.name, &r.tble, &r.sql); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// compareSchemas 比对两边的 schema，发现差异时报出具体对象。
func compareSchemas(fromVar, fromEmbed []schemaRow, version int) error {
	key := func(r schemaRow) string { return r.kind + ":" + r.name }

	varMap := make(map[string]schemaRow, len(fromVar))
	for _, r := range fromVar {
		varMap[key(r)] = r
	}
	embedMap := make(map[string]schemaRow, len(fromEmbed))
	for _, r := range fromEmbed {
		embedMap[key(r)] = r
	}

	var problems []string

	// 左有右无：导出时漏了。
	var keys []string
	for k := range varMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := embedMap[k]; !ok {
			problems = append(problems, fmt.Sprintf("内嵌脚本缺少对象 %s（旧变量里有）", k))
		}
	}

	// 右有左无：导出时多加了。
	keys = keys[:0]
	for k := range embedMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := varMap[k]; !ok {
			problems = append(problems, fmt.Sprintf("内嵌脚本多出对象 %s（旧变量里没有）", k))
		}
	}

	// 同名对象但定义不同：导出时改错了内容。
	keys = keys[:0]
	for k := range varMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := varMap[k]
		b, ok := embedMap[k]
		if !ok {
			continue
		}
		// 定义文本按空白折叠后比较：导出可能重排了缩进/换行，
		// 那不影响语义；但列名、类型、默认值、约束变了必须报出来。
		if collapseWS(a.sql) != collapseWS(b.sql) {
			problems = append(problems, fmt.Sprintf(
				"对象 %s 定义不同:\n      旧变量: %s\n      内嵌脚本: %s", k, a.sql, b.sql))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("v%d 的旧变量与内嵌脚本不一致（%d 处）:\n  - %s",
		version, len(problems), strings.Join(problems, "\n  - "))
}

// collapseWS 折叠空白用于比较定义文本（不影响语义的格式差异）。
func collapseWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// newMemoryDB 建一个单连接的内存库。
//
// 必须单连接：每个 :memory: 连接是独立的空库。
func newMemoryDB(t *testing.T) *sql.DB {
	t.Helper()
	node := gdb.ConfigNode{
		Type:                 "sqlite",
		Name:                 ":memory:",
		MaxOpenConnCount:     1,
		MaxIdleConnCount:     1,
		TimeMaintainDisabled: true,
	}
	inst, err := gdb.New(node)
	if err != nil {
		t.Fatalf("建内存库失败: %v", err)
	}
	db, err := inst.Master()
	if err != nil {
		t.Fatalf("取连接失败: %v", err)
	}
	t.Cleanup(func() { _ = inst.Close(context.Background()) })
	return db
}
