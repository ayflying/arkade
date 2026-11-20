package link

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"arkade/protocol/envelope"
)

func TestDialRejectsCredentialsAndLegacyURL(t *testing.T) {
	for _, address := range []string{
		"http://hub?token=secret", "https://user:secret@hub", "https://hub#secret",
		"wss://hub/agent/connect", "https://hub/agent/connect", "https://hub/%2f",
		"https://hub?", "http://hub:0", "http://hub:65536",
	} {
		if _, err := newDialer(address, "t", "", "1", 1, false); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("非法地址应被拒绝且错误不得回显密钥: %v", err)
		}
	}
}

func TestDialDiscoversPrefixedPathOnSameOrigin(t *testing.T) {
	const route = "/gateway/arkade" + envelope.AgentConnectPath
	mux := http.NewServeMux()
	check := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get(headerAuth) != "Bearer valid-token" || r.URL.RawQuery != "" {
			t.Error("凭据必须只出现在请求头")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc(envelope.AgentDiscoveryPath, func(w http.ResponseWriter, r *http.Request) {
		if check(w, r) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(envelope.AgentDiscovery{ConnectPath: route})
		}
	})
	mux.HandleFunc(route, func(w http.ResponseWriter, r *http.Request) {
		if check(w, r) {
			if conn, err := upgrader.Upgrade(w, r, nil); err == nil {
				_ = conn.Close()
			}
		}
	})
	srv := httptest.NewTLSServer(mux)
	defer srv.Close()
	d, err := newDialer(srv.URL, "valid-token", "", "test", envelope.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := d.dial(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

func TestDialStopsOnFailedOrInvalidDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		path   string
	}{
		{"unauthorized", http.StatusUnauthorized, ""},
		{"redirect", http.StatusTemporaryRedirect, "/redirected"},
		{"cross-origin", http.StatusOK, "//other.example/agent/connect"},
		{"encoded-path", http.StatusOK, "/prefix/%2e%2e/agent/connect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != envelope.AgentDiscoveryPath {
					t.Errorf("不应继续请求 %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Location", tc.path)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(envelope.AgentDiscovery{ConnectPath: tc.path})
			}))
			defer srv.Close()
			d, err := newDialer(srv.URL, "t", "", "test", envelope.Version, false)
			if err != nil {
				t.Fatal(err)
			}
			if _, resp, err := d.dial(t.Context()); err == nil || resp != nil {
				t.Fatalf("应在发现阶段终止：resp=%v err=%v", resp, err)
			}
		})
	}
}
