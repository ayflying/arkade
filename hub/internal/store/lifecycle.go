package store

// lifecycle.go 提供"关闭时等待后台任务"的闸门。
//
// Background ORM operations must finish before Close, otherwise GoFrame can
// create a new PostgreSQL pool after the owned pool has already been closed.
// Waiting for lifecycle tasks prevents those unowned server connections.
//
// ============================ 关闭协议 ============================
//	mu     保护 closed 与 wg 的 Add 之间的一致性
//	closed 置位后不再接受新任务
//	wg     追踪已接受、未结束的任务
//
// shutdown() 的顺序：持锁置 closed → 释放锁 → wg.Wait()。
// 关键点：**Add 与"置 closed"在同一把锁下**，所以置位之后不会再有人 Add，
// 否则并发 Add 与 Wait 会触发 sync.WaitGroup 的 "WaitGroup is reused
// before previous Wait has returned" 崩溃。

import (
	"context"
	"sync"
	"time"
)

// backgroundTimeout 是单个后台任务的执行上限。
//
// 必须有界：这任务在 Close 路径上被等待，若它因为锁竞争或磁盘问题挂住，
// Close 就会永远不返回，整个进程关不掉。超时后放弃本次清理，
// 下次写入时会再试（清理是幂等的）。
//
// 已知边界：shutdown 不主动取消已启动的任务，所以 Close 最坏等待
// backgroundTimeout 这么久。进程退出时若 ctx 已超时，这一项不受其约束。
// 需要更激进的收尾时，可让 gate 持有 cancel、shutdown 时先取消再 Wait。
const backgroundTimeout = 30 * time.Second

// lifecycleGate 是"关闭时等待后台任务"的闸门。
type lifecycleGate struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

// spawn 启动一个受管理的后台任务。
//
// 返回 false 表示已关闭，调用方应放弃这次任务。
// fn 收到的 ctx 带 backgroundTimeout 上限，且**不跟随传入 ctx 的取消**：
// 后台清理属于维护动作，调用方请求结束不代表清理应中断，
// 否则一次短请求就能让清理反复半途而废。它的寿命只由超时和关闭决定。
func (g *lifecycleGate) spawn(parent context.Context, fn func(ctx context.Context)) bool {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return false
	}
	// 在锁内 Add：与 shutdown 置 closed 互斥，
	// 保证 closed 之后不会再有 Add 与 Wait 并发。
	g.wg.Add(1)
	g.mu.Unlock()

	go func() {
		defer g.wg.Done()
		// 用有界 ctx：parent 被取消 或 超时，都让任务尽快收手。
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), backgroundTimeout)
		defer cancel()
		fn(ctx)
	}()
	return true
}

// shutdown 拦住新任务并等待已启动的任务结束。
//
// 返回后可以安全关闭底层连接：不会再有后台任务重新建连接。
func (g *lifecycleGate) shutdown() {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()

	// 在锁外 Wait：持锁等待会与任务内部可能的 spawn 调用构成死锁。
	// 此时 closed 已为 true，不可能再有人 Add。
	g.wg.Wait()
}
