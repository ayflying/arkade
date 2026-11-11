package dbmigrate

// legacy.go：旧库接管校验。
//
// ============================ 这个文件存在的理由 ============================
// Hub 现在有一套自己的迁移体系（schema_migrations 表 + migrate_sql.go）。
// 换成 golang-migrate 时，**已经部署的库**里既有真实业务数据，
// 又有旧体系的版本记录。
//
// 接管的风险在于：如果新体系对老库的理解有误（版本对不上、结构不一致），
// 轻则迁移跑错、重则数据损坏且无法恢复。
//
// 所以接管前必须逐项验证，任一项不符就**拒绝启动**——不是自动修复，
// 更不是 force。理由很实际：
//   - 自动修复会掩盖真实的损坏原因，下次还会犯；
//   - force 只是把版本号改成"看起来对"，结构错还是错，
//     而错误会在几天后以"某张表少一列"的形式爆出来，届时已无从追溯。
//
// ============================ 校验顺序很重要 ============================
// 所有校验都必须在**写入任何新表之前**完成。
// 否则一个被判定为异常的老库，也会被我们留下 arkade_migrate_versions 等痕迹，
// 等于污染了现场。

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// legacyTable 是旧迁移记录表的名字。
const legacyTable = "schema_migrations"

// legacyApplied 是旧表里的一条记录。
type legacyApplied struct {
	version   int
	name      string
	appliedAt int64
}

// inspectLegacy 检查旧迁移表的存在与内容。
//
// 返回 (记录列表, 旧表是否存在, 错误)。
// 全新库没有旧表是**正常**情况（返回 hasLegacy=false），不是错误。
func inspectLegacy(ctx context.Context, db *sql.DB) ([]legacyApplied, bool, error) {
	exists, err := tableExists(ctx, db, legacyTable)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}

	rows, err := db.QueryContext(ctx,
		`SELECT version, name, applied_at FROM `+legacyTable+` ORDER BY version`)
	if err != nil {
		return nil, true, fmt.Errorf("读取旧迁移记录失败: %w", err)
	}
	// ============================ 必须先 Close 再继续 ============================
	// 迁移连接池 MaxOpen=1。若此时不关 rows 就发下一条查询，
	// 下一条会等唯一那条连接，而 rows 还占着它 —— 永久死锁。
	defer rows.Close()

	var out []legacyApplied
	for rows.Next() {
		var a legacyApplied
		if err := rows.Scan(&a.version, &a.name, &a.appliedAt); err != nil {
			return nil, true, fmt.Errorf("扫描旧迁移记录失败: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, true, fmt.Errorf("遍历旧迁移记录失败: %w", err)
	}
	return out, true, nil
}

// validateLegacySequence 校验旧库的已应用版本是否连续、且不含未知版本。
//
// ============================ 判据 ============================
//  1. 版本号必须从 1 开始连续递增，不能有缺口。
//     缺口意味着"v3 没跑但 v4 跑了"，这在中途失败的库里可能出现，
//     继续接管会让缺失的表永远补不上。
//  2. 不能有超出内嵌迁移范围的版本。
//     出现 v11 说明这个库被**更新的程序**动过，
//     用当前版本的代码去操作它，等于降级运行，表结构可能对不上。
//
// 两条任一不满足 → 拒绝接管。
func validateLegacySequence(applied []legacyApplied, embedded []int) error {
	if len(applied) == 0 {
		// 旧表存在但为空：可以接管（等同于全新库），但要求内嵌版本从 1 开始。
		return nil
	}

	// 最大已知版本。
	maxEmbedded := 0
	if len(embedded) > 0 {
		maxEmbedded = embedded[len(embedded)-1]
	}
	known := make(map[int]struct{}, len(embedded))
	for _, v := range embedded {
		known[v] = struct{}{}
	}

	// ============================ 先查"未知版本"，再查"连续性" ============================
	// 顺序很重要：一个被更高版本程序升级过的库（有 v99）通常也"不连续"，
	// 但真正的问题是"程序太旧"，不是"库坏了"。先报连续性会把排查方向带偏。
	for _, a := range applied {
		if _, ok := known[a.version]; !ok {
			return fmt.Errorf(
				"旧库存在当前程序不认识的迁移版本 v%d（%s）；"+
					"这通常意味着数据库被更高版本的程序升级过，降级运行不安全，拒绝接管",
				a.version, a.name)
		}
	}

	// 再查连续性：版本号必须从 1 开始逐个递增。
	for i, a := range applied {
		want := i + 1
		if a.version != want {
			return fmt.Errorf(
				"旧库迁移版本不连续：第 %d 条记录的 version=%d，期望 %d（缺版本或顺序错乱）；"+
					"拒绝接管，请人工核查 %s 表",
				i, a.version, want, legacyTable)
		}
	}

	// 防御：内嵌版本比旧库版本少也拒绝（会导致后面的迁移无从执行）。
	if last := applied[len(applied)-1].version; last > maxEmbedded {
		return fmt.Errorf("旧库版本 v%d 超出内嵌迁移的最大版本 v%d，拒绝接管", last, maxEmbedded)
	}
	return nil
}

// schemaObject 是比对用的一个 schema 对象（表/索引/触发器）。
type schemaObject struct {
	kind string // table / index / trigger
	name string
	sql  string
}

// internalTables 是比对时需要排除的表。
//
// ============================ 为什么要排除 ============================
// "业务结构一致"的判断对象是**业务表**。下面这些表本来就该不同：
//   - schema_migrations：旧体系的记录表，新库（全新库）没有；
//   - arkade_migrate_versions：新体系的游标表，旧库接管前没有；
//   - sqlite_ 前缀：SQLite 内部表（sqlite_sequence 等），由引擎维护，
//     其存在与否取决于是否用过 AUTOINCREMENT，不在我们的控制范围。
//
// 把它们的差异算进比对，只会得到"永远不一致"的结论。
func isInternalTable(name string) bool {
	switch name {
	case legacyTable, MigrationsTable, ChecksumsTable:
		return true
	}
	if strings.HasPrefix(name, "sqlite_") {
		return true
	}
	return false
}

// readSchema 读出所有业务 schema 对象，并按统一格式归一化。
//
// ============================ 归一化规则 ============================
// SQLite 会把 CREATE 语句原样存进 sqlite_master，但不同路径建出来的表
// 可能在空白/大小写上不同（例如手工执行 vs 迁移执行）。
// 因此比对前做最小归一化：
//   - 折叠连续空白为单个空格；
//   - 去掉首尾空白；
//   - 统一转小写。
//
// **只做这三件事**，不动语义。多做一个动作都可能把真实差异洗掉。
//
// ============================ 排除内部对象的方式 ============================
// 关键是"按**表**排除，连同该表的索引/触发器一起排除"，而不是逐个判断名字。
//
// 为什么不能只按名字前缀过滤：SQLite 对 UNIQUE 约束会自动建索引，
// 名字形如 sqlite_autoindex_<表名>_<序号>；但**显式创建**的索引
// （CREATE INDEX / CREATE UNIQUE INDEX）名字是任意的，不带任何前缀。
//
// 具体到本包会遇到的情形：
//   - 新版本表 arkade_migrate_versions 由 golang-migrate 的 sqlite3 driver
//     建出，driver 自己也**显式创建**了一个名为 version_unique 的索引
//     （在 ensureVersionTable 里）。它不带 sqlite_ 前缀。
//   - 旧表 schema_migrations 的 version 是 INTEGER PRIMARY KEY，
//     那是 rowid 别名，**不会**自动建索引。
//
// 无论哪种情况，只要按 tbl_name 归属判断，就能正确排除"属于内部表的对象"，
// 而不会把新迁移器创建的索引误当成旧库的残留。
//
// 早期版本只按名字前缀过滤，漏掉了 version_unique，导致旧库接管时
// 报"实际库多一个索引"的假差异——那正是 TestLegacyTakeoverPreservesOldTable
// 抓到的 bug（该测试用的是旧原样格式的 fixture，没有新 migrator 污染）。
func readSchema(ctx context.Context, db *sql.DB) ([]schemaObject, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT type, name, COALESCE(tbl_name, ''), COALESCE(sql, '')
		FROM sqlite_master
		WHERE type IN ('table', 'index', 'trigger')
		ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("读取 schema 失败: %w", err)
	}
	defer rows.Close()

	var out []schemaObject
	for rows.Next() {
		var o schemaObject
		var tblName string
		if err := rows.Scan(&o.kind, &o.name, &tblName, &o.sql); err != nil {
			return nil, fmt.Errorf("扫描 schema 失败: %w", err)
		}

		// 内部表本身：排除。
		if isInternalTable(o.name) {
			continue
		}
		// 归属于内部表的索引/触发器：排除（含自动索引与具名约束索引）。
		if tblName != "" && isInternalTable(tblName) {
			continue
		}
		// SQLite 引擎自己的内部对象：排除。
		if strings.HasPrefix(o.name, "sqlite_") {
			continue
		}

		o.sql = normalizeSQL(o.sql)
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 schema 失败: %w", err)
	}
	return out, nil
}

// normalizeSQL 把 SQL 文本归一化到可比较的形式。
//
// ============================ 只折叠**代码区**的空白 ============================
// 早期版本直接对整串做 Fields + ToLower，这是错的：
//
//	CREATE TABLE t (s TEXT DEFAULT 'ABC')
//	CREATE TABLE t (s TEXT DEFAULT 'abc')
//
// 上面两条 SQL 语义**不同**（默认值不同），但全串 ToLower 会把它们
// 归一成同一串，于是真实的结构漂移被掩盖了。同理：
//
//	DEFAULT 'a  b'  与  DEFAULT 'a b'
//
// 两个不同常量，Fields 折叠空白后也会变成同一串。
//
// 正确做法是**只在字符串字面量之外**做归一化：
//   - 字面量内的内容逐字保留（包括大小写与连续空格）；
//   - 字面量外的空白折叠、关键字转小写。
//
// 这样既不放过真实差异，又能容忍格式差异（换行/缩进/关键字大小写）。
func normalizeSQL(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	inSingle := false   // 单引号字面量内
	inDouble := false   // 双引号标识符内
	inBacktick := false // 反引号标识符内
	pendingSpace := false

	flushSpace := func() {
		if pendingSpace && b.Len() > 0 {
			b.WriteByte(' ')
		}
		pendingSpace = false
	}

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]

		// 字面量/标识符内部：逐字保留，不做任何转换。
		if inSingle || inDouble || inBacktick {
			b.WriteRune(r)
			switch {
			case inSingle && r == '\'':
				// SQL 里 '' 表示转义的单引号，要连读两个。
				if i+1 < len(runes) && runes[i+1] == '\'' {
					b.WriteRune(runes[i+1])
					i++
				} else {
					inSingle = false
				}
			case inDouble && r == '"':
				inDouble = false
			case inBacktick && r == '`':
				inBacktick = false
			}
			continue
		}

		// 字面量/标识符之外。
		switch r {
		case '\'':
			flushSpace()
			b.WriteRune(r)
			inSingle = true
		case '"':
			flushSpace()
			b.WriteRune(r)
			inDouble = true
		case '`':
			flushSpace()
			b.WriteRune(r)
			inBacktick = true
		case ' ', '\t', '\n', '\r':
			// 空白：先记下来，等到下一个非空白字符时再决定是否写一个空格。
			// 这样能折叠"换行+缩进"为单个空格，同时不会在括号等符号周边留下奇怪空格。
			if b.Len() > 0 {
				pendingSpace = true
			}
		case '(', ')', ',':
			// 括号和逗号前不留空格，后面紧跟内容。
			pendingSpace = false
			b.WriteRune(r)
		default:
			flushSpace()
			// 只有字面量之外的标识符/关键字才转小写。
			b.WriteRune(toLowerRune(r))
		}
	}

	return strings.TrimSpace(b.String())
}

// toLowerRune 把 ASCII 大写字母转小写，其余字符原样返回。
//
// 只处理 ASCII：SQL 关键字都是 ASCII，非 ASCII 字符通常是中文标识符，
// 用 unicode.ToLower 去动它们反而有风险（不同语言的 case folding 规则不同）。
func toLowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + ('a' - 'A')
	}
	return r
}

// diffSchema 比对两份 schema，返回人类可读的差异描述。
//
// 返回空切片表示一致。
func diffSchema(want, got []schemaObject, wantLabel, gotLabel string) []string {
	wantMap := make(map[string]schemaObject, len(want))
	for _, o := range want {
		wantMap[o.kind+":"+o.name] = o
	}
	gotMap := make(map[string]schemaObject, len(got))
	for _, o := range got {
		gotMap[o.kind+":"+o.name] = o
	}

	var diffs []string

	// 期望有但实际没有。
	var missing []string
	for k := range wantMap {
		if _, ok := gotMap[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	for _, k := range missing {
		diffs = append(diffs, fmt.Sprintf("%s 中存在而 %s 中缺失: %s", wantLabel, gotLabel, k))
	}

	// 实际多出来的。
	var extra []string
	for k := range gotMap {
		if _, ok := wantMap[k]; !ok {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	for _, k := range extra {
		diffs = append(diffs, fmt.Sprintf("%s 中存在而 %s 中缺失: %s", gotLabel, wantLabel, k))
	}

	// 同名但定义不同。
	var changed []string
	for k, w := range wantMap {
		g, ok := gotMap[k]
		if !ok {
			continue
		}
		if w.sql != g.sql {
			changed = append(changed, k)
		}
	}
	sort.Strings(changed)
	for _, k := range changed {
		diffs = append(diffs, fmt.Sprintf("%s 定义不一致:\n    期望: %s\n    实际: %s",
			k, wantMap[k].sql, gotMap[k].sql))
	}

	return diffs
}

// tableExists 判断表是否存在。
func tableExists(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("检查表 %s 是否存在失败: %w", name, err)
	}
	return n > 0, nil
}
