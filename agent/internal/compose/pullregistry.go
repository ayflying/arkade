package compose

// pullregistry.go：任务表的生命周期（清理、关闭、ID 生成）。
//
// 与执行逻辑分开：清理规则（TTL、被谁触发）是独立的一条关注点，
// 它出错只会导致内存不回收，不会影响正在跑的任务。

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func (p *PullManager) sweepLocked() {
	now := time.Now()
	for id, task := range p.byID {
		task.mu.Lock()
		finished := !task.finishedAt.IsZero()
		age := now.Sub(task.createdAt)
		if finished {
			age = now.Sub(task.finishedAt)
		}
		task.mu.Unlock()
		if finished && age > PullTaskTTL {
			task.cancel()
			delete(p.byID, id)
			if task.requestID != "" {
				delete(p.byRequest, task.requestID)
			}
		}
	}
}

// Sweep 清理过期任务（可被定时调用，也供测试直接触发）。
func (p *PullManager) Sweep() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sweepLocked()
}

// Close 封闭新任务入口、取消全部任务，并等待执行 goroutine 完成。
// runner 的命令退出与 Wait 已在 goroutine 内完成，避免进程退出时遗留 docker 子进程。
// 有限等待由外层 Executor.Close(ctx) 提供，超时后后台继续回收。
func (p *PullManager) Close() {
	p.mu.Lock()
	p.closed = true
	tasks := make([]*PullTask, 0, len(p.byID))
	for _, t := range p.byID {
		tasks = append(tasks, t)
	}
	p.mu.Unlock()
	for _, t := range tasks {
		t.cancel()
	}
	p.workers.Wait()
}

// newPullTaskID 生成任务 ID（随机，不可预测）。
func newPullTaskID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 随机源不可用时退回时间戳：ID 只需在本进程内唯一，
		// 它不是凭证（不存在凭 ID 越权的路径——任务表只在本进程里）。
		return fmt.Sprintf("t%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// pullCommandDisplay 生成**真实执行的命令**展示串。
