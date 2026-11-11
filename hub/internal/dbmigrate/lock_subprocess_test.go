package dbmigrate

// lock_subprocess_test.go：用**真实子进程**验证跨进程互斥。
//
// ============================ 为什么必须用子进程 ============================
// goroutine 共享同一个进程、同一个文件描述符表。
// `TestRunSerializesConcurrentMigrations` 证明的是"进程内并发安全"，
// 而生产事故的场景是**多个进程**同时启动（容器重启、systemd 拉起、
// 运维手工再起一个）。
//
// 在同一进程里，flock 对**不同*os.File 实例**也确实会互斥，但那只是
// flock 的实现细节；真正要证明的是"操作系统级别的锁"。
// 因此这里 exec 一个真实的子进程去抢锁。

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// helperEnv 标记当前进程应作为"抢锁子进程"运行。
const helperEnv = "PROBE_MIGRATE_LOCK_HELPER"

// TestMain 支持把测试二进制自身当作子进程运行。
//
// 这是 Go 里测跨进程行为的标准做法：不用额外编译一个程序，
// 直接 exec 测试二进制本身，用环境变量区分角色。
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		// 子进程模式：尝试抢锁并报告结果，然后退出。
		runLockHelper()
		return
	}
	os.Exit(m.Run())
}

// runLockHelper 是子进程的入口。
//
// 行为：尝试获取指定库的迁移锁。
//   - 拿到锁 → 打印 "GOT" 并保持 1 秒后释放（模拟正在迁移）；
//   - 拿不到 → 打印 "BLOCKED" 并退出码 2。
func runLockHelper() {
	dbPath := os.Getenv("PROBE_MIGRATE_DB")
	if dbPath == "" {
		os.Exit(3)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	release, err := acquireLock(ctx, dbPath)
	if err != nil {
		// 拿不到锁是这个测试的正常分支之一。
		os.Stdout.WriteString("BLOCKED\n")
		os.Exit(2)
	}
	os.Stdout.WriteString("GOT\n")
	// 持有 1 秒，让父进程有机会来抢。
	time.Sleep(time.Second)
	release()
}

// TestLockBlocksRealSubprocess 验证子进程无法在锁被持有时进入。
//
// ============================ 这条测试证伪什么 ============================
// 若锁退化成"进程内互斥"（例如换成 sync.Mutex），子进程会拿到 GOT，
// 这条测试立刻失败。
func TestLockBlocksRealSubprocess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sub.db")

	// 父进程先持锁。
	release, err := acquireLock(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("父进程加锁失败: %v", err)
	}

	// 启动子进程尝试抢锁。
	cmd := exec.Command(os.Args[0], "-test.run=TestLockBlocksRealSubprocess")
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		"PROBE_MIGRATE_DB="+dbPath,
	)
	out, _ := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))

	// 先释放父进程的锁，避免后续步骤被阻塞。
	release()

	if !strings.Contains(result, "BLOCKED") {
		t.Fatalf("子进程在锁被持有时仍进入了迁移（输出=%q）——锁退化成进程内互斥", result)
	}
}

// TestLockFreeAfterReleaseSubprocess 释放后子进程应当能拿到锁。
//
// 这条与上一条配对：只证明"阻止"不够，还要证明"不误伤"。
// 若锁文件残留导致永久阻塞，这条会失败。
func TestLockFreeAfterReleaseSubprocess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "free.db")

	// 父进程加锁再释放。
	release, err := acquireLock(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("父进程加锁失败: %v", err)
	}
	release()

	// 子进程现在应当能拿到锁。
	cmd := exec.Command(os.Args[0], "-test.run=TestLockFreeAfterReleaseSubprocess")
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		"PROBE_MIGRATE_DB="+dbPath,
	)
	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))

	if err != nil {
		t.Fatalf("子进程未能拿到锁（输出=%q err=%v）——锁释放后仍阻塞", result, err)
	}
	if !strings.Contains(result, "GOT") {
		t.Fatalf("子进程输出异常: %q", result)
	}
}

// TestLockFileIsNotDeleted 验证锁文件不会被删除。
//
// ============================ 这条测试防的是什么 ============================
// 删除锁文件会破坏互斥性：等待者持着旧 inode 的 fd，
// 而新来的进程看到路径不存在就创建**新 inode** 并加锁成功，
// 于是两个进程同时"持有锁"。详见 lock.go 里的说明。
func TestLockFileIsNotDeleted(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "keep.db")
	lockPath := dbPath + lockSuffix

	release, err := acquireLock(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("加锁失败: %v", err)
	}
	release()

	// 释放之后锁文件必须仍然存在。
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("释放后锁文件被删除了（会破坏 Linux 上的 inode 互斥）: %v", err)
	}
}
