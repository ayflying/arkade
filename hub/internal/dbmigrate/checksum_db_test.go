package dbmigrate

// checksum_db_test.go：数据库内哈希记录的验证。
//
// ============================ 这些测试要证明什么 ============================
//   1. 迁移成功后，每个版本都在数据库里留下了哈希；
//   2. 数据库记录的哈希与当前脚本不一致 → 拒绝启动；
//   3. 内嵌基线被"一起改"时，数据库记录仍能挡住（这是内嵌基线做不到的）；
//   4. 老库首次引入本功能时，在结构验证通过后补齐登记；
//   5. 登记不会覆盖已有记录。
//
// 第 3 点是最关键的：它证明这道防线**独立于**内嵌基线而有效。

import (
	"context"
	"path/filepath"
	"testing"
)

// TestChecksumsRecordedAfterMigration 迁移成功后每个版本都应有哈希记录。
func TestChecksumsRecordedAfterMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.db")
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}

	db := openTestDB(t, path)
	recorded, err := loadRecordedChecksums(context.Background(), db)
	if err != nil {
		t.Fatalf("读哈希记录失败: %v", err)
	}

	current, err := computeChecksums()
	if err != nil {
		t.Fatalf("计算当前哈希失败: %v", err)
	}

	// 必须 10 个版本都有记录，且与当前脚本一致。
	if len(recorded) != len(current) {
		t.Fatalf("哈希记录数不符：记录了 %d 个，脚本有 %d 个", len(recorded), len(current))
	}
	for v, sum := range current {
		if recorded[v] != sum {
			t.Errorf("v%d 的记录哈希不符：记录 %s，当前 %s",
				v, shortHash(recorded[v]), shortHash(sum))
		}
	}
}

// TestRecordedChecksumMismatchRejected 数据库记录的哈希与脚本不一致应拒绝。
//
// ============================ 模拟的是什么场景 ============================
// 有人改了已发布的 v3 脚本，并且**同时**改了内嵌的 checksums.txt，
// 让两者自洽。此时内嵌基线检查通过，但数据库里记着执行当时的旧哈希，
// 于是这一层能挡住。
//
// 这里直接改数据库里的记录来模拟"脚本被改过"的等价效果——
// 结果相同：代码里的脚本与执行时不一致。
func TestRecordedChecksumMismatchRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mismatch.db")
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	db := openTestDB(t, path)

	// 把 v3 的记录改成一个不同的哈希（模拟脚本被改过）。
	const fakeHash = "0000000000000000000000000000000000000000000000000000000000000000"
	if _, err := db.Exec(`UPDATE `+ChecksumsTable+` SET checksum=? WHERE version=3`, fakeHash); err != nil {
		t.Fatalf("改记录失败: %v", err)
	}

	_, err := Run(context.Background(), path)
	if err == nil {
		t.Fatal("脚本哈希与执行时不一致，应拒绝启动")
	}
	if !containsAll(err.Error(), "v3", "不一致") {
		t.Fatalf("错误信息未指出 v3 不一致: %v", err)
	}

	// 记录不能被程序擅自"修正"。
	var sum string
	if err := db.QueryRow(`SELECT checksum FROM ` + ChecksumsTable + ` WHERE version=3`).Scan(&sum); err != nil {
		t.Fatalf("读记录失败: %v", err)
	}
	if sum != fakeHash {
		t.Fatal("程序不应自动改写哈希记录——那等于掩盖脚本被修改的事实")
	}
}

// TestSeedOnLegacyUpgrade 老库首次引入哈希记录时应补齐登记。
func TestSeedOnLegacyUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.db")

	// 先造一个"旧体系"的库并接管。
	db := openTestDB(t, path)
	applyLegacyUpTo(t, db, 10)
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("接管失败: %v", err)
	}

	// 模拟"首次引入本功能"：删掉哈希表。
	if _, err := db.Exec(`DROP TABLE ` + ChecksumsTable); err != nil {
		t.Fatalf("删哈希表失败: %v", err)
	}

	// 再次启动：应当补齐登记，而不是报错。
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("缺哈希记录时应能补齐登记: %v", err)
	}

	recorded, err := loadRecordedChecksums(context.Background(), db)
	if err != nil {
		t.Fatalf("读哈希记录失败: %v", err)
	}
	if len(recorded) != 10 {
		t.Fatalf("补齐后应有 10 条记录，实际 %d", len(recorded))
	}
}

// TestSeedRejectedWhenSchemaDrift 缺哈希记录但结构不对时，不能补齐。
//
// ============================ 这条守的是边界 ============================
// 补齐的前提是"结构已验证正确"。若结构对不上，
// 就没有资格假定"这些版本真的是按当前脚本执行的"——必须拒绝。
func TestSeedRejectedWhenSchemaDrift(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seeddrift.db")
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	db := openTestDB(t, path)

	// 删掉哈希表（模拟首次引入）+ 制造结构漂移。
	if _, err := db.Exec(`DROP TABLE ` + ChecksumsTable); err != nil {
		t.Fatalf("删哈希表失败: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE users ADD COLUMN sneaky TEXT NOT NULL DEFAULT ''`); err != nil {
		t.Fatalf("制造漂移失败: %v", err)
	}

	_, err := Run(context.Background(), path)
	if err == nil {
		t.Fatal("缺哈希记录且结构漂移时应拒绝，不能盲目补齐登记")
	}

	// 不应留下哈希表。
	ok, qerr := tableExists(context.Background(), db, ChecksumsTable)
	if qerr != nil {
		t.Fatalf("检查哈希表失败: %v", qerr)
	}
	if ok {
		t.Fatal("被拒绝时不应创建哈希表")
	}
}

// TestRecordDoesNotOverwrite 登记不应覆盖已有记录。
func TestRecordDoesNotOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noverwrite.db")
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	db := openTestDB(t, path)
	const fakeHash = "1111111111111111111111111111111111111111111111111111111111111111"
	if _, err := db.Exec(`INSERT OR REPLACE INTO `+ChecksumsTable+`(version,checksum,applied_at) VALUES(2,?,0)`, fakeHash); err != nil {
		t.Fatalf("预置记录失败: %v", err)
	}

	// 再跑一次（幂等路径），第 6 步会尝试登记但应 OR IGNORE 跳过 v2。
	if _, err := Run(context.Background(), path); err == nil {
		t.Fatal("v2 记录被篡改后应拒绝")
	}

	var sum string
	if err := db.QueryRow(`SELECT checksum FROM ` + ChecksumsTable + ` WHERE version=2`).Scan(&sum); err != nil {
		t.Fatalf("读记录失败: %v", err)
	}
	if sum != fakeHash {
		t.Fatal("登记逻辑覆盖了已有记录")
	}
}

// containsAll 检查 s 是否包含所有子串。
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// TestPartialMissingRecordedChecksums 部分缺失的记录也必须补齐并被核验。
//
// ============================ 这条守的漏洞 ============================
// 早期 needsSeeding 只判断"表无/表空"，于是"库到 v10 但哈希表只有 v1..v5"
// 会被误判为"已有基准"，v6..v10 从未被校验过——
// 一个被改过的 v8 脚本就能悄悄通过。
//
// 现在的要求：已执行的每个版本都要有记录；已有的记录一条都不能少核。
func TestPartialMissingRecordedChecksums(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.db")
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	db := openTestDB(t, path)

	// 删掉 v6..v10 的记录，模拟"部分缺失"（不删表）。
	if _, err := db.Exec(`DELETE FROM ` + ChecksumsTable + ` WHERE version >= 6`); err != nil {
		t.Fatalf("删部分记录失败: %v", err)
	}

	// 再次启动：应当补齐缺失的 v6..v10。
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("部分缺失时应能补齐: %v", err)
	}

	recorded, err := loadRecordedChecksums(context.Background(), db)
	if err != nil {
		t.Fatalf("读记录失败: %v", err)
	}
	if len(recorded) != 10 {
		t.Fatalf("补齐后应有 10 条记录，实际 %d: %v", len(recorded), recorded)
	}
}

// TestPartialMissingButExistingMismatchRejected 部分缺失时，已有的记录仍须核验。
//
// ============================ 这条是关键 ============================
// 走"补齐"分支不等于放行所有检查：
// **有记录的版本必须与当前脚本一致**，一条都不能少核。
// 否则只要删掉几条记录，就能让被改过的脚本逃过检查。
func TestPartialMissingButExistingMismatchRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partialbad.db")
	if _, err := Run(context.Background(), path); err != nil {
		t.Fatalf("首次迁移失败: %v", err)
	}

	db := openTestDB(t, path)

	// 删掉 v6..v10（制造"部分缺失"），同时把 v3 的记录改成假的。
	if _, err := db.Exec(`DELETE FROM ` + ChecksumsTable + ` WHERE version >= 6`); err != nil {
		t.Fatalf("删记录失败: %v", err)
	}
	const fakeHash = "2222222222222222222222222222222222222222222222222222222222222222"
	if _, err := db.Exec(`UPDATE `+ChecksumsTable+` SET checksum=? WHERE version=3`, fakeHash); err != nil {
		t.Fatalf("改记录失败: %v", err)
	}

	_, err := Run(context.Background(), path)
	if err == nil {
		t.Fatal("部分缺失时，已有记录不一致仍必须拒绝——否则删几条记录就能绕过检查")
	}
	if !containsAll(err.Error(), "v3", "不一致") {
		t.Fatalf("错误信息未指出 v3: %v", err)
	}
}
