package dbmigrate

// checksum_db.go：把已执行迁移脚本的哈希持久化到数据库。
//
// ============================ 为什么必须落库 ============================
// checksum.go 里的内嵌基线是**随代码分发**的，它和被检查的脚本在同一个
// 版本库里。改脚本的人同样能改基线——那一步之所以危险，是因为
// **已部署的库**是用旧脚本建的，而新代码里脚本变了，两边从此分叉。
//
// 内嵌基线挡得住"改了脚本忘记改基线"，挡不住"两个一起改"。
// 要真正闭合这个缺口，必须有一个**不在代码里**的参照物——
// 也就是数据库自己：每个版本执行时把当时的脚本哈希写进去，
// 以后每次启动都拿当前代码里的脚本去比。
//
// 这样"两个一起改"就会立刻暴露：数据库里记着旧哈希，代码里是新哈希。
//
// ============================ 表结构 ============================
//	CREATE TABLE arkade_migrate_checksums (
//	    version    INTEGER PRIMARY KEY,   -- 版本号
//	    checksum   TEXT    NOT NULL,      -- 该版本 up 脚本的 SHA256
//	    applied_at INTEGER NOT NULL       -- 登记时间（unix 秒）
//	);
//
// 单独一张表而不是塞进 arkade_migrate_versions（那是 migrate 的表，
// 结构由 driver 决定，我们不该往里加列）。这张表完全由本包管理。
//
// ============================ 写入时机 ============================
// 迁移成功之后。**不在每个版本执行后单独写**，而是整轮 Up 成功后
// 一次性把本轮所有新版本登记进去，原因：
//
//   - 迁移本身由 golang-migrate 在自己的事务里推进版本；
//   - 若我们逐版本插记录，一旦某个版本失败，会留下"版本记录有、
//     结构没建完"的半成品——正是 dirty 要防的情况。
//
// 整轮成功后写：失败时这张表不前进，下次启动重试时版本表会先报 dirty，
// 不会出现"哈希记录了但结构没到位"。
//
// ============================ 缺记录怎么办 ============================
// 已接管库若没有这张表（首次引入本功能），做**严格补齐**：
// 只有当结构比对（verifySchemaAt）完全通过时，才认为现有结构可信，
// 把当前版本的哈希补登记进去。结构对不上就拒绝，绝不"先记上再说"。
//
// 这个边界必须清楚：补齐的前提是"结构已验证正确"，
// 而不是"版本号说是 v10 所以应该是 v10"。

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ChecksumsTable 是迁移脚本哈希的持久化表名。
const ChecksumsTable = "arkade_migrate_checksums"

// ensureChecksumsTable 建哈希表（若不存在）。
func ensureChecksumsTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS `+ChecksumsTable+` (
			version    INTEGER PRIMARY KEY,
			checksum   TEXT    NOT NULL,
			applied_at INTEGER NOT NULL
		)`)
	if err != nil {
		return fmt.Errorf("建迁移哈希表失败: %w", err)
	}
	return nil
}

// loadRecordedChecksums 读出已登记的版本哈希。
func loadRecordedChecksums(ctx context.Context, db *sql.DB) (map[int]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT version, checksum FROM `+ChecksumsTable)
	if err != nil {
		return nil, fmt.Errorf("读取迁移哈希失败: %w", err)
	}
	// 必须先关 rows 再发后续查询：迁移连接池 MaxOpen=1。
	defer rows.Close()

	out := make(map[int]string)
	for rows.Next() {
		var v int
		var sum string
		if err := rows.Scan(&v, &sum); err != nil {
			return nil, fmt.Errorf("扫描迁移哈希失败: %w", err)
		}
		out[v] = sum
	}
	return out, rows.Err()
}

// verifyRecordedChecksums 比对"数据库里记的哈希"与"当前代码里的脚本哈希"。
//
// 只比对**数据库已登记**的版本：没登记的版本还没执行过，没有可比对的基准。
// 已登记但哈希不同 → 脚本被改过 → 拒绝。
func verifyRecordedChecksums(recorded, current map[int]string) error {
	var versions []int
	for v := range recorded {
		versions = append(versions, v)
	}
	sort.Ints(versions)

	var problems []string
	for _, v := range versions {
		want := recorded[v]
		got, ok := current[v]
		if !ok {
			problems = append(problems, fmt.Sprintf(
				"v%d 已在数据库中执行过，但当前代码里找不到该版本的脚本（脚本被删除？）", v))
			continue
		}
		if got != want {
			problems = append(problems, fmt.Sprintf(
				"v%d 的脚本与执行时不一致：执行时 %s，当前 %s",
				v, shortHash(want), shortHash(got)))
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf(
		"迁移脚本被修改过（共 %d 处），拒绝启动：\n  - %s\n"+
			"这些版本在数据库创建/升级时已经执行过，脚本内容不能改：\n"+
			"  改了它，已部署的库与新库的结构会永久分叉，而两边都不会报错。\n"+
			"请恢复脚本原内容；若确实需要改动结构，请新增一个版本。",
		len(problems), strings.Join(problems, "\n  - "))
}

// recordChecksums 把指定版本的哈希登记进数据库。
//
// ============================ 冲突必须显式处理 ============================
// 早期实现用 `INSERT OR IGNORE`，这对"已存在且相同"是合适的，
// 但它会**同时静默吞掉"已存在但不同"的情况**——
// 而那恰恰是最需要报警的：说明数据库里记的哈希与当前脚本不一致。
//
// 现在显式区分三种情况：
//   - 不存在          → 插入；
//   - 已存在且相同    → 跳过（幂等，正常）；
//   - 已存在但不同    → **报错**，绝不覆盖。
//
// 注意：正常启动路径上，不一致在步骤 2.5 就已经被 verifyRecordedChecksums
// 拦住了，不会走到这里。这里的检查是防御性的第二道：
// 万一校验与登记之间被并发改动（或将来有人只调用了本函数），也不会掩盖问题。
func recordChecksums(ctx context.Context, db *sql.DB, checksums map[int]string, versions []int) error {
	if len(versions) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("登记迁移哈希失败（开事务）: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, v := range versions {
		sum, ok := checksums[v]
		if !ok {
			return fmt.Errorf("登记迁移哈希失败：v%d 没有对应的校验和", v)
		}

		// 先查后写，在同一个事务里，避免并发下的 TOCTOU。
		var existing sql.NullString
		err := tx.QueryRowContext(ctx,
			`SELECT checksum FROM `+ChecksumsTable+` WHERE version=?`, v).Scan(&existing)
		switch {
		case err == sql.ErrNoRows:
			if _, ierr := tx.ExecContext(ctx,
				`INSERT INTO `+ChecksumsTable+`(version, checksum, applied_at)
				 VALUES(?, ?, unixepoch())`, v, sum); ierr != nil {
				return fmt.Errorf("登记 v%d 的哈希失败: %w", v, ierr)
			}
		case err != nil:
			return fmt.Errorf("登记 v%d 的哈希失败（读取现有记录）: %w", v, err)
		case existing.Valid && existing.String == sum:
			// 已登记且一致：幂等，跳过。
		default:
			// ============================ 不一致：必须报错 ============================
			// 不覆盖、不忽略。这说明脚本在执行后被改过。
			return fmt.Errorf(
				"登记 v%d 的哈希时发现冲突：数据库记录 %s，当前脚本 %s；\n"+
					"脚本在部署后被修改过，拒绝覆盖记录",
				v, shortHash(existing.String), shortHash(sum))
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("登记迁移哈希失败（提交）: %w", err)
	}
	return nil
}

// needsSeeding 判断哈希记录是否需要补齐。
//
// ============================ 不能只判断"表无/表空" ============================
// 早期实现是 `len(recorded) == 0`。这漏掉了**部分缺失**：
// 例如库已到 v10，但哈希表里只有 v1..v5。
// 那种情况下 `len != 0`，会被误判为"已有基准"，于是只校验 v1..v5，
// 而 v6..v10 从来没有被校验过——一个被改过的 v8 脚本就能悄悄通过。
//
// 正确判据：**已执行的每个版本都必须有记录**。
// 所以这里需要知道"已执行到哪个版本"，凡是在该范围内的版本缺记录，就需要补齐。
//
// 参数 upto 是当前数据库的版本（stateNew 时传 0，表示无需补齐）。
func needsSeeding(ctx context.Context, db *sql.DB, upto int) (bool, error) {
	if upto <= 0 {
		return false, nil
	}

	exists, err := tableExists(ctx, db, ChecksumsTable)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}

	recorded, err := loadRecordedChecksums(ctx, db)
	if err != nil {
		return false, err
	}
	embedded, err := embeddedVersions()
	if err != nil {
		return false, err
	}

	// 已执行范围内，任何一个版本缺记录就要补齐。
	for _, v := range embedded {
		if v > upto {
			break
		}
		if _, ok := recorded[v]; !ok {
			return true, nil
		}
	}
	return false, nil
}
