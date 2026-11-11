package dbmigrate

// lock_test.go：跨进程锁的行为验证。
//
// ============================ 这些测试要证明什么 ============================
//   1. 同一时刻只有一个持有者能拿到锁；
//   2. 释放之后别人能拿到；
//   3. 拿不到锁时 **明确报错**，而不是静默跳过迁移继续跑。
//
// 第 3 点最关键：跳过迁移意味着用旧结构跑新代码。
// 那种错误会在半天后以"某字段不存在"的形式爆出来，
// 届时没人会想到是启动时锁没拿到导致的。

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLockIsExclusive 同一库的锁在同一时刻只能被一个持有者拿到。
func TestLockIsExclusive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "lock.db")

	release1, err := acquireLock(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("第一次加锁失败: %v", err)
	}
	defer release1()

	// 第二次加锁必须失败（flock 在同一进程内对不同 flock 实例也会阻塞）。
	done := make(chan error, 1)
	go func() {
		// 用较短的超时避免测试等 30 秒。
		_, err := tryLockShort(dbPath)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("锁已被持有，第二次加锁却成功了")
		}
		if !strings.Contains(err.Error(), "迁移锁") {
			t.Fatalf("错误信息未提及迁移锁: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("第二个加锁操作既没成功也没失败（疑似死锁）")
	}
}

// TestLockReleasedAfterUnlock 释放后别人应当能拿到锁。
func TestLockReleasedAfterUnlock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "release.db")

	release, err := acquireLock(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("加锁失败: %v", err)
	}
	release()

	release2, err := acquireLock(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("释放后重新加锁失败: %v", err)
	}
	release2()
}

// TestRunSerializesConcurrentMigrations 并发调用 Run 不应互相破坏。
//
// ============================ 这条测试的意义 ============================
// 模拟"多个进程同时启动"：并发跑 Run，期望：
//   - 全部返回成功（或明确的可重试错误）；
//   - **最重要的是**：数据库最终不是 dirty 状态，且版本正确。
//
// 若没有跨进程锁，这里会看到 dirty=true 或版本错乱。
func TestRunSerializesConcurrentMigrations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "concurrent.db")

	const n = 4
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = Run(t.Context(), dbPath)
		}(i)
	}
	wg.Wait()

	// 至少一个成功。
	success := 0
	for i, err := range errs {
		if err == nil {
			success++
		} else {
			t.Logf("并发第 %d 个失败（可能是拿不到锁，属预期）: %v", i, err)
		}
	}
	if success == 0 {
		t.Fatal("并发迁移全部失败")
	}

	// 最终状态必须健康：不 dirty、版本到 10。
	db := openTestDB(t, dbPath)
	var version int
	var dirty int
	if err := db.QueryRow(`SELECT version, dirty FROM `+MigrationsTable).
		Scan(&version, &dirty); err != nil {
		t.Fatalf("读最终状态失败: %v", err)
	}
	if dirty != 0 {
		t.Fatalf("并发迁移把库推进了 dirty 状态（version=%d）", version)
	}
	if version != 10 {
		t.Fatalf("期望最终版本 10，实际 %d", version)
	}
}
