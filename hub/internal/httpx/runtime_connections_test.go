package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"arkade/hub/internal/agentlink"
)

func TestConnectionTrackerHonorsWaitDeadline(t *testing.T) {
	tracker := &connectionTracker{}
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		tracker.wrap(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(entered)
			<-release
		})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := tracker.close(ctx, agentlink.NewRegistry()); err != context.DeadlineExceeded {
		close(release)
		<-finished
		t.Fatalf("shutdown should report live handler: %v", err)
	}
	close(release)
	<-finished
	if err := tracker.close(context.Background(), agentlink.NewRegistry()); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionTrackerClosesUnregisteredHijackedSocket(t *testing.T) {
	tracker := &connectionTracker{}
	returned := make(chan struct{})
	ready := make(chan struct{})
	server := httptest.NewServer(tracker.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(returned)
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		close(ready)
		_, _, _ = conn.ReadMessage() // No Hello or Registry entry: shutdown must still wake it.
	})))
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+server.URL[len("http"):], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("upgrade handler did not become ready")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tracker.close(ctx, agentlink.NewRegistry()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned:
	default:
		t.Fatal("tracker returned before hijacked handler completed")
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("new request admitted after shutdown: %d", response.StatusCode)
	}
}
