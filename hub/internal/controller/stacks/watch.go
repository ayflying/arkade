package stacks

import (
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"sync"
	"time"
)

type operationWatchers struct {
	mu     sync.Mutex
	active map[string]bool
	ctx    context.Context
	cancel context.CancelFunc
	closed bool
	wg     sync.WaitGroup
	done   chan struct{}
}

func (m *operationWatchers) watch(d Deps, op store.ComposeOperation) {
	if op.Finalized || op.TaskID == "" {
		return
	}
	m.start(op, time.Second, d.Ops.ComposeOperationGet, d.DB.FinalizeComposeOperation)
}

// start keeps polling/finalization injectable without replacing the lifecycle logic.
func (m *operationWatchers) start(op store.ComposeOperation, interval time.Duration,
	pollTask func(context.Context, string, envelope.ComposeOperationGetPayload) (envelope.ComposeOperationResult, *agentlink.ConnError),
	finalize func(context.Context, store.ComposeOperation, bool) (store.ComposeOperation, error)) {
	key := op.AgentID + "/" + op.TaskID
	m.mu.Lock()
	if m.closed || op.Finalized || op.TaskID == "" {
		m.mu.Unlock()
		return
	}
	if m.active == nil {
		m.active = map[string]bool{}
		m.ctx, m.cancel = context.WithCancel(context.Background())
	}
	if m.active[key] || len(m.active) >= 64 {
		m.mu.Unlock()
		return
	}
	m.active[key] = true
	m.wg.Add(1) // under the same mutex as close: no Add/Wait race
	parent := m.ctx
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer func() { m.mu.Lock(); delete(m.active, key); m.mu.Unlock() }()
		ctx, cancel := context.WithTimeout(parent, 12*time.Minute)
		defer cancel()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if ctx.Err() != nil {
				return
			}
			poll, stop := context.WithTimeout(ctx, 5*time.Second)
			task, e := pollTask(poll, op.AgentID, envelope.ComposeOperationGetPayload{TaskID: op.TaskID, After: 1 << 62})
			stop()
			if e != nil {
				continue
			}
			if task.TaskID != op.TaskID || task.Name != op.Name || task.Action != op.Action {
				return
			}
			if task.Status == "succeeded" || task.Status == "failed" {
				if _, e := finalize(ctx, op, task.Status == "succeeded"); e == nil {
					return
				}
			}
		}
	}()
}
