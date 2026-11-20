package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/protocol/envelope"
)

func TestAgentDiscoveryRoute(t *testing.T) {
	const token = "route-test-token"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := agentlink.NewRegistry()
	agents := agentlink.NewServer(reg, token, "test", nil, log)
	discover, err := agents.DiscoveryHandler("/edge")
	if err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Deps{Log: log, Registry: reg, Ops: agentlink.NewOps(reg), AgentDiscovery: discover, AgentConn: http.HandlerFunc(agents.HandleConnect)})
	router.SetAddr("127.0.0.1:0")
	router.SetGraceful(false)
	router.SetSessionCookieOutput(false)
	if err := router.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = router.Shutdown() })
	client := &http.Client{Timeout: 3 * time.Second}
	for _, tc := range []struct {
		path, key string
		want      int
	}{
		{envelope.AgentDiscoveryPath, "", http.StatusUnauthorized},
		{envelope.AgentDiscoveryPath, token, http.StatusOK},
		{envelope.AgentConnectPath, "invalid-key", http.StatusUnauthorized},
	} {
		req, err := http.NewRequest(http.MethodGet, "http://"+router.GetListenedAddress()+tc.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if tc.key != "" {
			req.Header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+tc.key)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var result envelope.AgentDiscovery
		decodeErr := json.NewDecoder(resp.Body).Decode(&result)
		_ = resp.Body.Close()
		if resp.StatusCode != tc.want {
			t.Fatalf("%s status=%d want=%d", tc.path, resp.StatusCode, tc.want)
		}
		if tc.want == http.StatusOK && (decodeErr != nil || result.ConnectPath != "/edge"+envelope.AgentConnectPath) {
			t.Fatalf("invalid discovery %+v: %v", result, decodeErr)
		}
	}
}
