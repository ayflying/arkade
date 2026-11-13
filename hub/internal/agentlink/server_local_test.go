package agentlink

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arkade/protocol/envelope"
	"github.com/gorilla/websocket"
)

type localProtectionHandler struct{ writes atomic.Int32 }

func (h *localProtectionHandler) OnAgentHello(context.Context, string, envelope.HelloPayload) error {
	h.writes.Add(1)
	return nil
}
func (*localProtectionHandler) OnAgentHeartbeat(context.Context, envelope.HeartbeatPayload) {}
func (*localProtectionHandler) OnAgentSnapshot(context.Context, string, []envelope.ContainerSummary) {
}
func (*localProtectionHandler) OnAgentDisconnected(context.Context, string) {}

type localProtectionCredentials struct{ id, hash string }

func (c localProtectionCredentials) LookupAgentByTokenHash(hash string) (string, error) {
	if hash == c.hash {
		return c.id, nil
	}
	return "", ErrTokenNotEnrolled
}
func (c localProtectionCredentials) HasAgentToken(string) (bool, error) { return false, nil }

func TestServerRejectsReservedLocalBeforeHelloWrite(t *testing.T) {
	for _, dedicated := range []bool{false, true} {
		for _, guarded := range []bool{false, true} {
			name := map[bool]string{false: "shared", true: "old-dedicated"}[dedicated] + map[bool]string{false: "-unguarded", true: "-guarded"}[guarded]
			t.Run(name, func(t *testing.T) {
				registry := NewRegistry()
				const id = "reserved-hub-local"
				if err := registry.ReserveLocalID(id); err != nil {
					t.Fatal(err)
				}
				handler := &localProtectionHandler{}
				log := slog.New(slog.NewTextHandler(io.Discard, nil))
				server := NewServer(registry, "shared-token", "test", handler, log)
				server.SetCredentialStore(localProtectionCredentials{id: id, hash: HashEnrollmentToken("old-local-token")})
				if guarded {
					server.SetEnrollmentGuard(NewEnrollmentGuard())
				}
				httpServer := httptest.NewServer(http.HandlerFunc(server.HandleConnect))
				defer httpServer.Close()
				token := "shared-token"
				headerID := id
				if dedicated {
					token = "old-local-token"
					headerID = "ignored-attacker-header"
				}
				headers := http.Header{}
				headers.Set(envelope.HeaderAuthorization, envelope.BearerPrefix+token)
				headers.Set(envelope.HeaderAgentID, headerID)
				ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(httpServer.URL, "http"), headers)
				if err != nil {
					t.Fatal(err)
				}
				defer ws.Close()
				ws.SetReadDeadline(time.Now().Add(2 * time.Second))
				hello, err := envelope.NewRequest("local-spoof", envelope.MethodHello, "hub", envelope.HelloPayload{Hostname: "attacker"})
				if err != nil {
					t.Fatal(err)
				}
				if err = ws.WriteJSON(hello); err != nil {
					t.Fatal(err)
				}
				var result envelope.Envelope
				if err = ws.ReadJSON(&result); err != nil {
					t.Fatal(err)
				}
				if result.Error == nil || result.Error.Code != envelope.CodeForbidden || !strings.Contains(result.Error.Message, "本机") {
					t.Fatalf("远程冒充未明确拒绝: %+v", result)
				}
				if handler.writes.Load() != 0 {
					t.Fatal("拒绝前调用了握手写库hook")
				}
				if registry.IsOnline(id) || !registry.IsReservedLocal(id) {
					t.Fatal("远程握手改变本机身份保留状态")
				}
			})
		}
	}
}
