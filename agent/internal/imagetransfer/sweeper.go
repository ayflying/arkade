package imagetransfer

// sweeper.go 提供后台过期清理。
//
// ============================ 为什么必须有它 ============================
//
// 传输临时区里躺着的是**几 GB 的镜像归档**。它们只在三种时机被删除：
//
//  1. 显式 release（Hub 主动释放）；
//  2. 任务成功（load 完成后立即删）；
//  3. 超时过期 —— 这一条只有后台清扫器能触发。
//
// 缺了第 3 条会发生什么：Hub 崩了、浏览器关了、用户直接走了。
// 此时没人会来调 release，几 GB 归档就永久占着磁盘。
// 更糟的是配额是**硬约束**——垃圾占满之后，后续所有传输都会被拒绝，
// 而用户完全看不出原因（他看不到 transfers 目录）。
//
// 因此清扫器不是"优化"，是配额机制能自我恢复的前提。

import (
	"context"
	"time"
)

// SweepInterval 是清扫周期。
//
// 取 1 分钟：TTL 是 10/30 分钟级别，分钟级清扫足够及时；
// 再密只是徒增唤醒，对磁盘占用没有任何实质改善。
const SweepInterval = time.Minute

// RunSweeper 启动后台清扫，直到 ctx 取消。
//
// 阻塞式：调用方应当以 goroutine 方式运行它。
// 用一个 ticker 而不是 time.AfterFunc 递归：前者在 ctx 取消时能立刻退出，
// 后者需要额外的停止逻辑，且容易在退出后仍触发一次。
func (m *Manager) RunSweeper(ctx context.Context) {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Sweep()
		}
	}
}
