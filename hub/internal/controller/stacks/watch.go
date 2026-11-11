package stacks

import (
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"sync"
	"time"
)

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
			poll, stop := context.WithTimeout(ctx, 5*time.Second)
			task, e := d.Ops.ComposeOperationGet(poll, op.AgentID, envelope.ComposeOperationGetPayload{TaskID: op.TaskID, After: 1 << 62})
			stop()
			if e != nil {
				continue
			}
			if task.TaskID != op.TaskID || task.Name != op.Name || task.Action != op.Action {
				return
			}
			if task.Status == "succeeded" || task.Status == "failed" {
				if _, e := d.DB.FinalizeComposeOperation(ctx, op, task.Status == "succeeded"); e == nil {
					return
				}
			}
		}
	}()
}
