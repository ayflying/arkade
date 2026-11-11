package httpx

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"sync"

	"arkade/hub/internal/agentlink"
)

// connectionTracker joins hijacked Agent handlers, which HTTP Shutdown does not wait for.
// Admission and WaitGroup.Add share a lock so shutdown cannot race with new handlers.
type connectionTracker struct {
	mu      sync.Mutex
	closing bool
	active  sync.WaitGroup
	sockets map[net.Conn]struct{}
}

type trackedUpgradeWriter struct {
	http.ResponseWriter
	tracker *connectionTracker
	conn    net.Conn
}

func (w *trackedUpgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.tracker.mu.Lock()
	defer w.tracker.mu.Unlock()
	if w.tracker.closing {
		_ = conn.Close()
		return nil, nil, net.ErrClosed
	}
	if w.tracker.sockets == nil {
		w.tracker.sockets = make(map[net.Conn]struct{})
	}
	w.tracker.sockets[conn] = struct{}{}
	w.conn = conn
	return conn, rw, nil
}

func (w *trackedUpgradeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (t *connectionTracker) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.mu.Lock()
		if t.closing {
			t.mu.Unlock()
			http.Error(w, "Hub is shutting down", http.StatusServiceUnavailable)
			return
		}
		t.active.Add(1)
		t.mu.Unlock()
		writer := &trackedUpgradeWriter{ResponseWriter: w, tracker: t}
		defer func() {
			t.mu.Lock()
			delete(t.sockets, writer.conn)
			t.mu.Unlock()
			t.active.Done()
		}()
		next.ServeHTTP(writer, r)
	})
}

func (t *connectionTracker) close(ctx context.Context, registry *agentlink.Registry) error {
	t.mu.Lock()
	t.closing = true
	connections := make([]net.Conn, 0, len(t.sockets))
	for conn := range t.sockets {
		connections = append(connections, conn)
	}
	t.mu.Unlock()
	// Includes sockets still waiting for Hello; they are not yet in Registry.
	for _, conn := range connections {
		_ = conn.Close()
	}
	for id := range registry.OnlineIDs() {
		registry.Close(id, "Hub is shutting down")
	}
	done := make(chan struct{})
	go func() {
		t.active.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
