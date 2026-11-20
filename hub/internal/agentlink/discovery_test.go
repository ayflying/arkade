package agentlink

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkade/protocol/envelope"
	"github.com/gorilla/websocket"
)

func TestDisabledDedicatedAgentRejectedBeforeUpgrade(t *testing.T) {
	const token, id = "dedicated-test-token", "disabled-agent"
	s := NewServer(NewRegistry(), "", "test", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetCredentialStore(localProtectionCredentials{id: id, hash: HashEnrollmentToken(token)})
	s.SetAccessCheck(func(_ context.Context, got string) error {
		if got != id {
			t.Errorf("expected credential-bound identity, got %q", got)
		}
		return errors.New("disabled")
	})
	discover, err := s.DiscoveryHandler("")
	if err != nil {
		t.Fatal(err)
	}
	for path, handler := range map[string]http.Handler{
		envelope.AgentDiscoveryPath: discover,
		envelope.AgentConnectPath:   http.HandlerFunc(s.HandleConnect),
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+token)
		req.Header.Set(envelope.HeaderAgentID, "untrusted-id")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s: got %d, want 403 before upgrade", path, w.Code)
		}
	}
}

func TestDiscoveryAndUpgradeAuthenticateIndependently(t *testing.T) {
	const token = "discovery-test-token"
	s := NewServer(NewRegistry(), token, "test", nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	discover, err := s.DiscoveryHandler("/gateway/arkade")
	if err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"", "wrong-token", token} {
		req := httptest.NewRequest(http.MethodGet, envelope.AgentDiscoveryPath, nil)
		req.Header.Set("X-Forwarded-Prefix", "/ignored")
		if key != "" {
			req.Header.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+key)
		}
		w := httptest.NewRecorder()
		discover.ServeHTTP(w, req)
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q", got)
		}
		if key != token {
			if w.Code != http.StatusUnauthorized || strings.Contains(w.Body.String(), "connect_path") {
				t.Fatalf("unauthorized discovery: %d %s", w.Code, w.Body.String())
			}
			continue
		}
		var response envelope.AgentDiscovery
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK ||
			response.ConnectPath != "/gateway/arkade"+envelope.AgentConnectPath {
			t.Fatalf("valid discovery: %d %+v %v", w.Code, response, err)
		}
	}

	// 即便已知路径，也不能只靠此前的发现请求或 URL 里的密钥升级连接。
	srv := httptest.NewServer(http.HandlerFunc(s.HandleConnect))
	defer srv.Close()
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+envelope.AgentConnectPath+"?token="+token, nil)
	if conn != nil {
		_ = conn.Close()
	}
	if resp != nil {
		defer resp.Body.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("WebSocket 认证应在升级前拒绝：%v %v", resp, err)
	}
}
