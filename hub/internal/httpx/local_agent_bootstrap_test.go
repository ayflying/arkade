package httpx

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/config"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
)

// 通过真实生产启动验证空库预建、专属凭据 WebSocket 握手和重启幂等。
func TestLocalAgentBootstrapNewApp(t *testing.T) {
	var (
		ctx = context.Background()
		cfg = config.Default()
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	)
	cfg.Database.DSN = testdb.New(t)
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	cfg.Auth.InitialUser = "admin"
	cfg.Auth.InitialPass = "1"
	cfg.Auth.AgentToken = "bootstrap-shared-token-only"
	cfg.Auth.LocalAgentToken = "bootstrap-local-dedicated-token-only"
	cfg.Web.Dist = ""

	var original store.AgentRecord
	for _, phase := range []string{"fresh", "restart"} {
		t.Run(phase, func(t *testing.T) {
			app, err := NewApp(ctx, cfg, log, "local-bootstrap-test")
			if err != nil {
				t.Fatalf("NewApp %s: %v", phase, err)
			}
			t.Cleanup(func() {
				shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if err := app.Shutdown(shutdownCtx); err != nil {
					t.Errorf("shutdown %s: %v", phase, err)
				}
			})

			// 在监听前就应只有一个预建服务器，且专属凭据已完成绑定。
			agents, err := app.db.ListAgents(ctx)
			if err != nil || len(agents) != 1 {
				t.Fatalf("expected exactly one bootstrapped agent: %+v, err=%v", agents, err)
			}
			rec := agents[0]
			if rec.ID != "agent-local" || rec.Name != "本机" || rec.DisplayName != "本机" || !rec.Preprovisioned || rec.Disabled {
				t.Fatalf("unexpected local agent: %+v", rec)
			}
			if phase == "fresh" {
				if rec.Status != store.StatusPending {
					t.Fatalf("new local agent must be pending, got %q", rec.Status)
				}
				original = rec
			} else if rec.CreatedAt != original.CreatedAt {
				t.Fatalf("restart recreated local agent: before=%+v after=%+v", original, rec)
			}
			id, err := app.db.LookupAgentByTokenHash(ctx, agentlink.HashEnrollmentToken(cfg.Auth.LocalAgentToken))
			if err != nil || id != "agent-local" {
				t.Fatalf("dedicated credential not bound before listening: id=%q err=%v", id, err)
			}
			users, err := app.db.CountUsers(ctx)
			if err != nil || users != 1 {
				t.Fatalf("expected exactly one initial administrator: count=%d err=%v", users, err)
			}

			app.server.SetAddr("127.0.0.1:0")
			if err := app.server.Start(); err != nil {
				t.Fatal(err)
			}
			baseURL := "http://" + app.server.GetListenedAddress()
			agent, ack := dialAsAgent(t, baseURL, cfg.Auth.LocalAgentToken)
			defer agent.conn.Close()
			if ack.AgentID != "agent-local" {
				t.Fatalf("dedicated token handshake assigned wrong identity: %+v", ack)
			}
			agents, err = app.db.ListAgents(ctx)
			if err != nil || len(agents) != 1 || agents[0].ID != "agent-local" || agents[0].Name != "本机" || agents[0].DisplayName != "本机" {
				t.Fatalf("hello created a duplicate or overwrote the local name: %+v err=%v", agents, err)
			}

			// 验证真实登录接口和会话 Cookie，而非仅验证密码哈希。
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
			response, err := client.Post(baseURL+"/api/auth/login", "application/json", strings.NewReader(`{"username":"admin","password":"1"}`))
			if err != nil {
				t.Fatal(err)
			}
			var login userEnvelope
			err = json.NewDecoder(response.Body).Decode(&login)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusOK || login.User.Username != "admin" {
				t.Fatalf("initial password 1 login failed: status=%d response=%+v err=%v", response.StatusCode, login, err)
			}
			response, err = client.Get(baseURL + "/api/auth/me")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("login cookie did not authenticate /me: status=%d", response.StatusCode)
			}
		})
		if t.Failed() {
			return
		}
		// 重启时不能用部署初始密码覆盖数据库已有账号。
		cfg.Auth.InitialPass = "must-not-replace-initial-password"
	}
}
