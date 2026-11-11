package stacks

import (
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/store"
	"arkade/protocol/envelope"
	"context"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatcherOutlivesHTTPServerRegression(t *testing.T) {
	server := httptest.NewServer(nil)
	var watchers operationWatchers
	calls := make(chan struct{}, 16)
	var joined atomic.Bool
	watchers.start(store.ComposeOperation{AgentID: "agent", TaskID: "task", Name: "app", Action: "up"}, time.Millisecond,
		func(context.Context, string, envelope.ComposeOperationGetPayload) (envelope.ComposeOperationResult, *agentlink.ConnError) {
			if joined.Load() {
				calls <- struct{}{}
			}
			return envelope.ComposeOperationResult{TaskID: "task", Name: "different"}, nil
		}, nil)
	// Regression was first observed without this lifecycle join: HTTP close
	// alone left the independent background RPC loop alive.
	server.Close()
	if err := watchers.close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A poll that started before Close joined is legal. Only dispatches after
	// the join receipt violate the shutdown contract, regardless of scheduling.
	joined.Store(true)
	select {
	case <-calls:
		t.Fatal("background watcher dispatched RPC after HTTP shutdown")
	case <-time.After(20 * time.Millisecond):
	}
}
