package httpx

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/config"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
)

// 通过生产装配入口验证预建先于监听，且无需给 Agent 写固定身份。
func TestRuntimeLocalAgentBootstrap(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = testdb.New(t)
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	cfg.Auth.InitialPass = "local-test"
	cfg.Auth.LocalAgentToken = "local-runtime-token-0123456789"
	cfg.Web.Dist = ""
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(ctx, cfg, log, "local-bootstrap-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(ctx) })
	if id, err := app.db.LookupAgentByTokenHash(ctx, agentlink.HashEnrollmentToken(cfg.Auth.LocalAgentToken)); err != nil || id != store.LocalAgentID {
		t.Fatalf("监听前必须已有绑定: %q %v", id, err)
	}
	app.server.SetAddr("127.0.0.1:0")
	if err := app.server.Start(); err != nil {
		t.Fatal(err)
	}
	url := "http://" + app.server.GetListenedAddress()
	conn, ack := dialWith(t, url, cfg.Auth.LocalAgentToken, "stale-agent-id", "实际主机名")
	_ = conn.Close()
	if ack.AgentID != store.LocalAgentID {
		t.Fatalf("专属密钥未绑定固定记录: %+v", ack)
	}
	rec, err := app.db.GetAgent(ctx, store.LocalAgentID)
	if err != nil || rec.Name != store.LocalAgentName || rec.DisplayName != store.LocalAgentName {
		t.Fatalf("握手覆盖预建名称: %+v %v", rec, err)
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// 相同配置可重启，换环境密钥则必须在构造阶段失败，而非监听后才发现。
	restarted, err := NewApp(ctx, cfg, log, "local-restart-test")
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	cfg.Auth.LocalAgentToken = "rotated-runtime-token-0123456789"
	failed, err := NewApp(ctx, cfg, log, "local-conflict-test")
	if failed != nil {
		_ = failed.Shutdown(ctx)
		t.Fatal("冲突不得返回可运行实例")
	}
	if err == nil || strings.Contains(err.Error(), cfg.Auth.LocalAgentToken) {
		t.Fatalf("应安全报告凭据冲突: %v", err)
	}
}

func TestRuntimeLocalAgentOptional(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = testdb.New(t)
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	cfg.Auth.InitialPass = "local-test"
	cfg.Web.Dist = ""
	app, err := NewApp(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "without-local-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(ctx)
	recs, err := app.db.ListAgents(ctx)
	if err != nil || len(recs) != 0 {
		t.Fatalf("未配置不得自动创建: %+v %v", recs, err)
	}
}
