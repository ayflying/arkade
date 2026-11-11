package dbmigrate

// run.go：迁移的执行入口与状态判定。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
)

// Result 描述一次迁移执行的结果。
type Result struct {
	// FromVersion / ToVersion 是执行前后的版本（-1 表示此前无版本）。
	FromVersion int
	ToVersion   int

	// Dirty 是执行结束后的脏标记。
	Dirty bool

	// Applied 是本次实际执行的版本数。
	Applied int

	// Adopted 表示本次执行**首次**完成了旧体系接管。
	Adopted bool

	// AdoptedPending 是内部状态：本次需要执行首次接管（尚未 force）。
	AdoptedPending bool

	// LegacyMax 是旧记录表里的最高版本（仅接管时使用）。
	LegacyMax int
}

// state 是一次迁移开始前，对数据库所处状态的判定结果。
//
// ============================ 为什么要有这个类型 ============================
// 迁移的起点有三种情况，处理方式完全不同：
//
//	new        —— 全新库：从零开始跑全部迁移；
//	managed    —— 已被新体系管理：以新版本表为唯一权威；
//	legacyOnly —— 只有旧记录表：首次接管。
//
// 早期版本把"旧表存在"直接当成 legacyOnly，导致一个**严重的升级 bug**：
// 旧表在接管后是永久保留的（那是历史记录，不能删），
// 于是下次启动仍然走 legacyOnly 分支，拿旧表的 legacyMax（v10）
// 去比对当前结构（已经升到 v11），结构必然"不一致"，于是拒绝启动。
//
// **也就是说，只要升级过一次新版本，应用就再也起不来了。**
// 因此状态判定必须**先看新版本表**，新版本表存在时它就是权威。
type dbState int

const (
	// stateNew 是全新库：新版本表与旧记录表都没有。
	stateNew dbState = iota
	// stateManaged 是已被新体系管理：新版本表存在。
	// **以它记录的 version 为唯一权威**，旧表不再参与任何判定。
	stateManaged
	// stateLegacyOnly 是旧体系遗留库：只有旧记录表，新版本表不存在。
	stateLegacyOnly
)

// stateName 返回状态的中文名，用于日志与错误信息。
func stateName(s dbState) string {
	switch s {
	case stateNew:
		return "全新库"
	case stateManaged:
		return "已接管库"
	case stateLegacyOnly:
		return "旧体系库"
	default:
		return "未知状态"
	}
}

// Run 执行迁移：状态判定 → 校验 → 推进。
//
// ============================ 执行顺序（严格） ============================
//  1. 拿跨进程锁（尊重调用方的 ctx，可被取消）。
//  2. 判定状态：先看新版本表，再看旧记录表。
//  3. 按状态分别校验：
//     - managed：检查 dirty、检查版本是否超出内嵌范围、按当前版本校验结构；
//     - legacyOnly：校验旧版本序列、结构漂移；
//     - new：检查是否存在"没有记录表却有业务表"的异常库。
//  4. 以上全部通过后，才允许写入（建新版本表 / force / up）。
func Run(ctx context.Context, path string) (*Result, error) {
	// 先拿跨进程锁。锁必须尊重调用方 ctx：
	// 调方取消时应尽快返回，而不是死等 30 秒。
	release, err := acquireLock(ctx, path)
	if err != nil {
		return nil, err
	}
	defer release()

	conn, err := openMigrationConn(path)
	if err != nil {
		return nil, err
	}
	// 迁移连接是独立的短生命周期连接，绝不与业务池共用。
	defer conn.close()

	return runOn(ctx, conn)
}

// runOn 在给定连接上执行迁移（便于测试直接注入）。
func runOn(ctx context.Context, conn *connect) (*Result, error) {
	res := &Result{FromVersion: -1, ToVersion: -1}

	// ---------- 0. 校验和基线 ----------
	//
	// ============================ 为什么放在最前面 ============================
	// 这是"迁移脚本自身是否可信"的检查，与被迁移的库无关。
	// 脚本被改过时应该**立刻**报错，而不是等到结构比对才发现
	// "库和脚本对不上"——那时候错误信息会指向库，而真正的原因在脚本。
	//
	// 这条检查不读写数据库，纯 CPU，代价可忽略。
	if err := verifyChecksums(); err != nil {
		return nil, err
	}

	embedded, err := embeddedVersions()
	if err != nil {
		return nil, err
	}

	// ---------- 1. 状态判定 ----------
	st, err := detectState(ctx, conn.db)
	if err != nil {
		return nil, err
	}

	// ---------- 2. 校验（全部在写入之前，不构造 migrator） ----------
	//
	// ============================ 为什么这里不能构造 migrator ============================
	// newMigrator 会创建 arkade_migrate_versions 表——那是一次写入。
	// 若在校验之前构造，一个被判定为异常的库也会被留下我们的表，
	// 等于污染现场（Lead 的回归测试 TestReviewRejectUnversionedBusinessDatabase
	// 就是断言这一点）。
	//
	// 因此 managed 状态也不能用 m.Version() 读游标，改为**直接查表**：
	// 表已存在，纯 SELECT 不产生任何写入。见 readManagedVersion。
	switch st {
	case stateManaged:
		if err := checkManaged(ctx, conn, embedded, res); err != nil {
			return nil, err
		}
	case stateLegacyOnly:
		if err := checkLegacyOnly(ctx, conn, embedded, res); err != nil {
			return nil, err
		}
	case stateNew:
		if err := checkNew(ctx, conn.db); err != nil {
			return nil, err
		}
	}

	// ---------- 2.5 数据库内记录的脚本哈希校验 ----------
	//
	// ============================ 与内嵌基线的分工 ============================
	// 内嵌基线（步骤 0）挡的是"改了脚本忘了改基线"——同一版本库内的自洽性。
	// 这里挡的是"脚本和基线一起被改"——那是内嵌基线**看不见**的，
	// 因为参照物在代码里，和脚本同生共死。
	//
	// 数据库里记的是"执行当时的哈希"，是代码之外的参照物。
	// 两者不一致说明脚本在部署后被改过，已部署的库与新库会永久分叉。
	//
	// ============================ 校验策略 ============================
	// **已登记的记录一条都不能少核**，与是否"部分缺失"无关：
	// 有一條就核一条，不一致立刻拒绝。
	//
	// 缺记录的版本不盲信——它们没有可比对的基准。这些版本靠两道关兜住：
	//   1. 步骤 2 的结构验证（verifySchemaAt）确认真实结构与这些版本一致；
	//   2. 步骤 6 在迁移成功、结构已确认后才补齐登记。
	//
	// 注意：这一步在 stateNew 时不适用（全新库还没执行过任何版本）。
	if st != stateNew {
		current, err := computeChecksums()
		if err != nil {
			return nil, err
		}
		recorded, err := loadRecordedChecksums(ctx, conn.db)
		if err != nil {
			// 表不存在时 loadRecordedChecksums 会报错，那是正常情况（首次引入），
			// 不视为失败——由步骤 6 建表并补齐。
			exists, terr := tableExists(ctx, conn.db, ChecksumsTable)
			if terr != nil {
				return nil, terr
			}
			if exists {
				return nil, err
			}
			recorded = map[int]string{}
		}
		// 已有的记录必须与当前脚本一致，一条都不能少核。
		if err := verifyRecordedChecksums(recorded, current); err != nil {
			return nil, err
		}
	}

	// ---------- 3. 构造 migrator（从这里开始才允许写） ----------
	m, err := newMigrator(conn)
	if err != nil {
		return nil, err
	}
	defer func() { _, _ = m.Close() }()

	// ---------- 4. 旧库首次接管：把新体系对齐到旧库已达版本 ----------
	//
	// 只有 legacyOnly 状态才需要这一步。已接管库的游标本来就在新版本表里，
	// 绝不能再用旧表的 legacyMax 去覆盖它——那正是升级 bug 的根源。
	if res.AdoptedPending {
		if err := m.Force(res.LegacyMax); err != nil {
			return nil, fmt.Errorf("标记旧库已达版本 v%d 失败: %w", res.LegacyMax, err)
		}
		res.FromVersion = res.LegacyMax
		res.Adopted = true
	}

	// ---------- 5. 执行迁移 ----------
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		if v, dirty, verr := m.Version(); verr == nil {
			res.ToVersion = int(v)
			res.Dirty = dirty
		}
		return res, fmt.Errorf("执行迁移失败（数据库可能已进入 dirty 状态，需人工介入）: %w", err)
	}

	v, dirty, verr := m.Version()
	if verr != nil && !errors.Is(verr, migrate.ErrNilVersion) {
		return res, fmt.Errorf("读取迁移后版本失败: %w", verr)
	}
	res.ToVersion = int(v)
	res.Dirty = dirty

	// ============================ Applied 的计算 ============================
	// **不用版本号相减**。那只有在"版本是连续整数 1..N"时才成立：
	//   - 未来若改用时间戳版本（20261111120000），相减就毫无意义；
	//   - 版本号里有空洞时，相减得到的也不是"执行了几个"。
	//
	// 改成数内嵌序列里有多少个版本落在 (FromVersion, ToVersion] 区间内。
	// 这才是"本次实际执行了几个迁移文件"的定义，且对版本号形态无假设。
	//
	// 前提约束：**内嵌版本必须是严格递增的稠密序列**（当前为 1..10），
	// 这由 TestEmbeddedVersionsAreContiguous 守住。
	res.Applied = countVersionsInRange(embedded, res.FromVersion, res.ToVersion)

	// ---------- 6. 登记脚本哈希 ----------
	//
	// ============================ 写入时机与崩溃窗口 ============================
	// 放在整轮 Up 成功之后一次性写，范围是 ToVersion 覆盖的全部内嵌版本。
	//
	// 已知的**崩溃窗口**：版本表已推进、哈希记录还没写时进程崩溃，
	// 下次启动会看到"版本表说 v10，哈希表缺 6-10"。
	// 兜底是结构验证：下次启动在补登记前先用 verifySchemaAt 比对真实结构与
	// v10 的期望结构，一致才补登记，不一致就拒绝。
	//
	// 边界：结构验证只能证明 DDL 到位，证明不了数据迁移正确。
	// 当前 1-10 全部是 DDL（已 grep 确认无 INSERT/UPDATE/DELETE，
	// 只有 ON DELETE CASCADE 外键子句），所以这条兜底是够用的；
	// 将来若引入数据变换，需要更强的机制。
	//
	// 逐版登记并不能消除这个窗口：它只是把窗口从"整轮"缩到"单版本"，
	// 跨事务的间隙依然存在。这里选整轮成功后一次性写。
	if err := ensureChecksumsTable(ctx, conn.db); err != nil {
		return res, err
	}
	current, err := computeChecksums()
	if err != nil {
		return res, err
	}
	// 登记 ToVersion 覆盖的全部版本（而非仅本次新执行的）：
	// 这样"部分缺失"的老库能一次补齐，不必等下一轮。
	covered := embedded[:countVersionsInRange(embedded, -1, res.ToVersion)]
	if err := recordChecksums(ctx, conn.db, current, covered); err != nil {
		return res, err
	}

	return res, nil
}

// countVersionsInRange 统计 versions 中落在 (from, to] 区间内的个数。
//
// from 为 -1 表示"此前无版本"，此时统计所有 <= to 的版本。
func countVersionsInRange(versions []int, from, to int) int {
	n := 0
	for _, v := range versions {
		if v > from && v <= to {
			n++
		}
	}
	return n
}

// detectState 判定数据库当前处于哪种状态。
//
// ============================ 判定顺序是关键 ============================
// **先看新版本表，再看旧记录表。**
//
// 因为旧表在接管后永久保留（那是历史记录），一个已经升级过多个版本的库
// 会同时拥有两张表。此时它属于 managed，旧表只是历史遗迹，
// 不应参与任何版本/结构判定。
func detectState(ctx context.Context, db *sql.DB) (dbState, error) {
	hasNew, err := tableExists(ctx, db, MigrationsTable)
	if err != nil {
		return stateNew, err
	}
	if hasNew {
		return stateManaged, nil
	}

	hasLegacy, err := tableExists(ctx, db, legacyTable)
	if err != nil {
		return stateNew, err
	}
	if hasLegacy {
		return stateLegacyOnly, nil
	}
	return stateNew, nil
}

// checkManaged 校验已被新体系管理的库。
//
// ============================ 这里为什么不再看旧表 ============================
// 新版本表是唯一权威。旧表只作为历史参考，不参与版本比对，
// 也不参与结构比对——否则就会复现"拿 v10 比 v11 拒绝启动"的升级 bug。
// checkManaged 校验已被新体系管理的库。
//
// ============================ 这里为什么不再看旧表 ============================
// 新版本表是唯一权威。旧表只作为历史参考，不参与版本比对，
// 也不参与结构比对——否则就会复现"拿 v10 比 v11 拒绝启动"的升级 bug。
//
// ============================ 为什么不用 m.Version() ============================
// 构造 migrator 会创建版本表（写入）。校验必须在任何写入之前完成，
// 因此这里**直接 SELECT 版本表**——表已经存在，纯读不产生副作用。
func checkManaged(ctx context.Context, conn *connect, embedded []int, res *Result) error {
	v, dirty, err := readManagedVersion(ctx, conn.db)
	if err != nil {
		return err
	}

	res.FromVersion = v

	// ============================ dirty 必须拒绝 ============================
	if dirty {
		return fmt.Errorf(
			"数据库处于 dirty 状态（版本 v%d）：上次迁移执行失败，结构可能不完整；"+
				"请人工核查并修复后，显式执行 force 再重启", v)
	}

	// ============================ 未知更高版本必须拒绝 ============================
	maxEmbedded := 0
	if len(embedded) > 0 {
		maxEmbedded = embedded[len(embedded)-1]
	}
	if v > maxEmbedded {
		return fmt.Errorf(
			"数据库版本 v%d 高于当前程序支持的最高版本 v%d；"+
				"这通常意味着数据库被更高版本的程序升级过，降级运行不安全，拒绝启动",
			v, maxEmbedded)
	}
	known := false
	for _, e := range embedded {
		if e == v {
			known = true
			break
		}
	}
	if !known {
		return fmt.Errorf("数据库版本 v%d 不在当前程序的迁移序列中，拒绝启动", v)
	}

	// ============================ 按当前版本校验结构 ============================
	// 用当前版本 v 重建期望结构来比——**不是**旧表的 legacyMax。
	if err := verifySchemaAt(ctx, conn.db, v); err != nil {
		return err
	}
	return nil
}

// readManagedVersion 直接读取新版本表的游标（纯读，无副作用）。
//
// ============================ 版本表长什么样 ============================
// golang-migrate 的 sqlite3 driver 在 ensureVersionTable 里建的是：
//
//	CREATE TABLE IF NOT EXISTS arkade_migrate_versions (version uint64, dirty bool);
//	CREATE UNIQUE INDEX IF NOT EXISTS version_unique ON arkade_migrate_versions (version);
//
// **注意：version 不是主键**，只是一个带 UNIQUE 索引的普通列。
// 因此表里**可能存在多行**（例如 version=1 和 version=10 并存）。
// 早期实现用 `LIMIT 1` 取第一行，这等于在多行时**随机挑一个**，
// 甚至可能挑到旧的 v1 而放过真实的 v10——必须严格拒绝。
//
// 正常运行时 migrate 的 SetVersion 会先 DELETE 再 INSERT，表里应恰好一行。
// 多行说明有人手工改过，或并发写入绕过了锁。任何一种都不能猜，只能报错。
//
// 同时校验：
//   - 恰好一行（0 行 = 初始化中断；>1 行 = 状态不确定）；
//   - dirty 只能是 0/1（driver 里是 bool，但 SQLite 不做类型约束，
//     手工能写进任意整数）；
//   - version 不能为负、不能为 NULL。
func readManagedVersion(ctx context.Context, db *sql.DB) (int, bool, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT version, dirty FROM `+MigrationsTable)
	if err != nil {
		return 0, false, fmt.Errorf("读取迁移版本失败: %w", err)
	}
	// 必须先关 rows 再发后续查询：迁移连接池 MaxOpen=1，
	// 不关就会在下一条查询上永久等待。
	defer rows.Close()

	type row struct {
		version sql.NullInt64
		dirty   sql.NullInt64
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.version, &r.dirty); err != nil {
			return 0, false, fmt.Errorf("扫描迁移版本失败: %w", err)
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return 0, false, fmt.Errorf("遍历迁移版本失败: %w", err)
	}

	// ============================ 行数必须恰好为 1 ============================
	switch len(all) {
	case 0:
		return 0, false, fmt.Errorf(
			"新版本表 %s 存在但没有版本记录：上次初始化可能中断，请人工核查", MigrationsTable)
	case 1:
		// 正常。
	default:
		// 多行时把版本号都列出来，便于人工判断该保留哪一行。
		var vs []string
		for _, r := range all {
			if r.version.Valid {
				vs = append(vs, fmt.Sprintf("%d", r.version.Int64))
			} else {
				vs = append(vs, "NULL")
			}
		}
		return 0, false, fmt.Errorf(
			"新版本表 %s 中存在 %d 行版本记录（%v），正常应恰好一行；"+
				"状态不确定，拒绝启动，请人工核查后只保留正确的那一行",
			MigrationsTable, len(all), vs)
	}

	r := all[0]

	// ============================ version 校验 ============================
	if !r.version.Valid {
		return 0, false, fmt.Errorf(
			"新版本表 %s 的 version 为 NULL，拒绝启动，请人工核查", MigrationsTable)
	}
	if r.version.Int64 < 0 {
		return 0, false, fmt.Errorf(
			"新版本表 %s 的 version 为负数（%d），拒绝启动，请人工核查",
			MigrationsTable, r.version.Int64)
	}

	// ============================ dirty 校验 ============================
	// driver 里 dirty 是 bool，但 SQLite 是动态类型，手工能写进任意值。
	// 只接受 0/1：其它值说明这份数据不是 driver 写的，不能按 bool 解释。
	if !r.dirty.Valid {
		return 0, false, fmt.Errorf(
			"新版本表 %s 的 dirty 为 NULL，拒绝启动，请人工核查", MigrationsTable)
	}
	switch r.dirty.Int64 {
	case 0:
		return int(r.version.Int64), false, nil
	case 1:
		return int(r.version.Int64), true, nil
	default:
		return 0, false, fmt.Errorf(
			"新版本表 %s 的 dirty 值非法（%d，只允许 0 或 1），拒绝启动，请人工核查",
			MigrationsTable, r.dirty.Int64)
	}
}

// checkLegacyOnly 校验只有旧记录表的库（首次接管场景）。
func checkLegacyOnly(ctx context.Context, conn *connect, embedded []int, res *Result) error {
	applied, hasLegacy, err := inspectLegacy(ctx, conn.db)
	if err != nil {
		return err
	}
	if !hasLegacy {
		// detectState 刚说存在，这里却读不到：状态在两次读之间变了。
		return fmt.Errorf("旧记录表状态不一致，请重试")
	}

	// 旧表存在但没有记录：不能当空库处理。
	//
	// ============================ 为什么必须拒绝 ============================
	// 旧表存在说明这套库曾经被旧版 Hub 使用过，但一行版本记录都没有。
	// 正常情况下这是不可能的（旧版启动时第一件事就是建表写 v1）。
	// 出现这种情况意味着：
	//   - 表被清空过（有人手工 DELETE）；
	//   - 或者数据库是从别处拷来的残片。
	// 无论哪种，直接当空库重跑迁移都会在"表已存在"上失败，
	// 或者更糟——在不完整结构上继续 ALTER。所以拒绝，让人来看。
	if len(applied) == 0 {
		if err := rejectIfHasBusinessData(ctx, conn.db); err != nil {
			return err
		}
	}

	if err := validateLegacySequence(applied, embedded); err != nil {
		return err
	}

	// 结构漂移校验：必须在任何写入之前完成。
	if len(applied) > 0 {
		if err := verifySchemaAt(ctx, conn.db, applied[len(applied)-1].version); err != nil {
			return err
		}
		res.AdoptedPending = true
		res.LegacyMax = applied[len(applied)-1].version
	}
	return nil
}

// checkNew 校验全新库的合理性。
//
// ============================ 没有记录表但有业务表 ============================
// 这是最容易被忽略的一种异常：库文件存在、里面有一堆业务表，
// 但既没有新版本表也没有旧记录表。
//
// 正常途径**不可能**产生这种库（任何版本的 Hub 启动都会先建记录表）。
// 它通常意味着：
//   - 有人手工建了表；
//   - 或者记录表被删了。
//
// 此时若当空库处理，迁移会在 CREATE TABLE 上失败；
// 若加上 IF NOT EXISTS 让它"成功"，又会在 ALTER TABLE 上失败，
// 而且库会进入 dirty 状态。所以必须提前拒绝。
func checkNew(ctx context.Context, db *sql.DB) error {
	business, err := listBusinessTables(ctx, db)
	if err != nil {
		return err
	}
	if len(business) == 0 {
		return nil // 真正的空库，正常。
	}
	return fmt.Errorf(
		"数据库中存在业务表但没有迁移记录表（既无 %s 也无 %s）：%v；"+
			"这不可能是正常启动产生的，通常意味着记录表被删除或表是手工建的；"+
			"拒绝启动，请人工确认该库的来源",
		MigrationsTable, legacyTable, business)
}

// rejectIfHasBusinessData 在"旧表为空"的情况下，检查是否已有业务表。
//
// 旧表为空但业务表有内容 → 拒绝（记录丢失，无从判断实际版本）。
func rejectIfHasBusinessData(ctx context.Context, db *sql.DB) error {
	business, err := listBusinessTables(ctx, db)
	if err != nil {
		return err
	}
	if len(business) == 0 {
		return nil
	}
	return fmt.Errorf(
		"旧记录表 %s 为空但库中已有业务表：%v；"+
			"无法判断这些表对应的迁移版本（记录可能被清空），拒绝启动，请人工核查",
		legacyTable, business)
}

// listBusinessTables 列出所有业务表（排除版本记录表与 SQLite 内部表）。
func listBusinessTables(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("读取表清单失败: %w", err)
	}
	// 必须先关 rows 再发后续查询：迁移连接池 MaxOpen=1，
	// 不关就会在下一条查询上永久等待。
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("扫描表名失败: %w", err)
		}
		if isInternalTable(name) {
			continue
		}
		out = append(out, name)
	}
	return out, rows.Err()
}
