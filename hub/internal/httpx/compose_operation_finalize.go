package httpx

import (
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"sync"
	"time"
)

// 每个路由实例共享有界watch表；关闭浏览器不影响Hub完成同步标记或条件删除。
// Agent任务上限32，watch最多64；网络断开后继续只读查询，不重新发起执行。
type operationWatchers struct {
	mu     sync.Mutex
	active map[string]bool
}

func (m *operationWatchers) watch(d Deps, op store.ComposeOperation) {
	if op.Finalized || op.TaskID == "" {
		return
	}
	key := op.AgentID + "/" + op.TaskID
	m.mu.Lock()
	if m.active == nil {
		m.active = map[string]bool{}
	}
	if m.active[key] || len(m.active) >= 64 {
		m.mu.Unlock()
		return
	}
	m.active[key] = true
	m.mu.Unlock()
	go func() {
		defer func() { m.mu.Lock(); delete(m.active, key); m.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			pollCtx, stopPoll := context.WithTimeout(ctx, 5*time.Second)
			task, e := d.Ops.ComposeOperationGet(pollCtx, op.AgentID, envelope.ComposeOperationGetPayload{TaskID: op.TaskID, After: 1 << 62})
			stopPoll()
			if e != nil {
				continue
			}
			if task.TaskID != op.TaskID || task.Name != op.Name || task.Action != op.Action {
				return
			}
			if task.Status == "succeeded" || task.Status == "failed" {
				if _, err := d.DB.FinalizeComposeOperation(ctx, op, task.Status == "succeeded"); err == nil {
					return
				}
			}
		}
	}()
}
