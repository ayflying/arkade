package dbmigrate

// lock.go：迁移的跨进程文件锁。
//
// ============================ 为什么必须用文件锁 ============================
// Hub 可能被多个进程同时启动（容器重启、systemd 拉起、运维手工再起一个）。
// 若两个进程同时执行迁移：
//
//	A: 读版本=9，决定跑 v10
//	B: 读版本=9，决定跑 v10
//	A: 执行 CREATE TABLE ...（成功）
//	B: 执行 CREATE TABLE ...（报"表已存在"，或被迁移框架判为 dirty）
//
// 结果是数据库进入 dirty 状态，而两个进程谁都不知道"其实是并发造成的"。
//
// ============================ 为什么不能用 sync.Mutex ============================
// 进程内锁只在本进程有效。上面那个场景是两个**不同进程**，
// 各自持有自己的 Mutex，互相看不见。文件锁是操作系统级别的，
// 对所有进程生效。
//
// ============================ 为什么锁文件放在库文件旁边 ============================
// 锁必须随着"数据库"走，而不是随着"进程的工作目录"走。
// 若锁放在 cwd，两个进程用不同 cwd 启动同一个库时锁就失效了。
// 放在库文件同目录（<db>.migrate.lock）保证：**同一个库永远竞争同一把锁**。
//
// 这也解释了测试为什么要用临时目录里的独立库：
// 每个测试库有自己独立的锁文件，测试之间不会互相阻塞。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// lockSuffix 是锁文件名后缀。
const lockSuffix = ".migrate.lock"

// lockWait 是等待锁的超时时间。
//
// ============================ 这个值怎么定的 ============================
// 迁移通常几秒内完成；但如果另一个进程正在执行长迁移（例如大表 ALTER），
// 等待者不应该立刻失败——那会让"容器重启时偶发启动失败"变成常态。
//
// 30 秒足够覆盖正常迁移，又能在真正卡死时及时报错而不是无限挂起。
// 超时后必须**报错拒绝启动**，而不是直接跳过迁移继续跑：
// 跳过迁移意味着用旧结构跑新代码，比启动失败危险得多。
const lockWait = 30 * time.Second

// acquireLock 获取跨进程迁移锁。
//
// 返回的 release 函数必须在迁移结束后调用（defer）。
// 若在超时时间内拿不到锁，返回错误——调用方应当**拒绝启动**。
func acquireLock(ctx context.Context, dbPath string) (release func(), err error) {
	// 锁文件与库同目录，保证同一库的所有进程竞争同一把锁。
	abs, aerr := filepath.Abs(dbPath)
	if aerr != nil {
		return nil, fmt.Errorf("解析数据库路径失败: %w", aerr)
	}
	lockPath := abs + lockSuffix

	fl := flock.New(lockPath)

	// ============================ 超时来自调用方 ctx，但有上限 ============================
	// 不用阻塞式 Lock()：锁被长时间持有时会无限挂起，
	// 容器就永远卡在启动阶段（"起不来也不报错"是最难排查的一类故障）。
	//
	// 用调用方 ctx 而不是 Background：调用方取消（例如启动流程超时）时
	// 应尽快返回，而不是死等。
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	defer cancel()

	locked, err := fl.TryLockContext(lockCtx, 100*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("获取迁移锁失败（锁文件 %s）: %w", lockPath, err)
	}
	if !locked {
		return nil, fmt.Errorf(
			"无法获取数据库迁移锁（%s）：另一个进程正在执行迁移，或上次迁移异常退出后仍持有锁；"+
				"请确认没有其他 Hub 实例正在启动后重启", lockPath)
	}

	release = func() {
		// ============================ 只释放句柄，绝不删除锁文件 ============================
		// 删除锁文件会**破坏互斥性**，这是一个在 Linux 上真实发生的竞争：
		//
		//	A: 释放锁，删除路径 /db.migrate.lock
		//	B: 此刻已打开旧 inode 并在等待（它已经拿到旧文件的 fd）
		//	C: 看到路径不存在，创建**新 inode** 的锁文件并加锁成功
		//
		//	结果 B 和 C 各持一个"锁"，同时进入迁移 —— 互斥失效。
		//
		// 保留锁文件（哪怕进程退出后只剩一个空文件）没有任何代价：
		// flock 的语义是"文件描述符上的锁"，空文件不会阻止下次加锁。
		// 因此这里**只关闭句柄**。
		if uerr := fl.Unlock(); uerr != nil {
			// 解锁失败不影响数据正确性（锁会在进程退出时由系统自动释放），
			// 但要打出来，便于排查"为什么下次启动还在等锁"。
			fmt.Fprintf(os.Stderr, "警告: 释放迁移锁失败: %v\n", uerr)
		}
	}
	return release, nil
}

// tryLockShort 用很短的超时尝试加锁，仅用于测试。
//
// 生产路径永远用 acquireLock 的 30 秒超时；这里单独实现是为了让
// "锁已被占用"这条测试不必等 30 秒。
func tryLockShort(dbPath string) (func(), error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("解析数据库路径失败: %w", err)
	}
	lockPath := abs + lockSuffix
	fl := flock.New(lockPath)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	locked, lerr := fl.TryLockContext(ctx, 50*time.Millisecond)
	if lerr != nil {
		return nil, fmt.Errorf("获取迁移锁失败（锁文件 %s）: %w", lockPath, lerr)
	}
	if !locked {
		return nil, fmt.Errorf("无法获取数据库迁移锁（%s）：已被其他进程持有", lockPath)
	}
	return func() { _ = fl.Unlock() }, nil
}
