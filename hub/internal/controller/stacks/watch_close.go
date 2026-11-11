package stacks

import "context"

// Close stops accepting watchers, cancels active polling/finalization and joins
// all workers before the App releases Agent connections or the database.
// A timed-out wait is retryable: cancellation remains in effect and the same
// completion channel is reused. It is a package function because GF Bind treats
// every exported controller method as a potential HTTP action.
func Close(ctx context.Context, c *Controller) error { return c.watchers.close(ctx) }

func (m *operationWatchers) close(ctx context.Context) error {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		if m.cancel != nil {
			m.cancel()
		}
		m.done = make(chan struct{})
		go func() { m.wg.Wait(); close(m.done) }()
	}
	done := m.done
	m.mu.Unlock()
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
