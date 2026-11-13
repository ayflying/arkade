package httpx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/config"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
)

// 生产入口复用旧本机身份，但撤销网络凭据；不再通过 token 握手启动本机。
func TestRuntimeLocalAgentBootstrap(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = testdb.New(t)
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	cfg.Auth.InitialPass = "local-test"
	cfg.Auth.LocalAgentToken = "local-runtime-token-0123456789"
	cfg.Local.Enabled = true
	cfg.Local.Runtime = "none"
	cfg.Local.DataDir = t.TempDir()
	cfg.Web.Dist = ""
	legacyHash := agentlink.HashEnrollmentToken(cfg.Auth.LocalAgentToken)
	db, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.EnsureLocalAgent(ctx, legacyHash); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	app, err := NewApp(ctx, cfg, log, "local-bootstrap-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Shutdown(ctx) })
	if id, err := app.db.LookupAgentByTokenHash(ctx, legacyHash); !errors.Is(err, store.ErrAgentTokenNotFound) || id != "" {
		t.Fatalf("旧本机网络凭据必须撤销: %q %v", id, err)
	}
	rec, err := app.db.GetAgent(ctx, store.LocalAgentID)
	if err != nil || rec.Name != store.LocalAgentName || rec.DisplayName != store.LocalAgentName {
		t.Fatalf("内置本机未保留原身份名称: %+v %v", rec, err)
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	// 旧环境 token 改变不再构成凭据冲突，也不能重新绑定已撤销的旧 token。
	cfg.Auth.LocalAgentToken = "rotated-runtime-token-0123456789"
	restarted, err := NewApp(ctx, cfg, log, "local-restart-test")
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Shutdown(ctx)
	reloaded, err := restarted.db.GetAgent(ctx, store.LocalAgentID)
	if err != nil || reloaded.ID != rec.ID || reloaded.CreatedAt != rec.CreatedAt {
		t.Fatalf("重启不得重建本机: before=%+v after=%+v err=%v", rec, reloaded, err)
	}
	if has, err := restarted.db.HasAgentToken(ctx, store.LocalAgentID); err != nil || has {
		t.Fatalf("旧配置 token 不得绑定内置本机: %v %v", has, err)
	}
}

func TestRuntimeLocalAgentOptional(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = testdb.New(t)
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	cfg.Auth.InitialPass = "local-test"
	cfg.Auth.LocalAgentToken = "ignored-old-local-token-0123456789"
	cfg.Local.Enabled = false
	cfg.Local.Runtime = "none"
	cfg.Local.DataDir = t.TempDir()
	cfg.Web.Dist = ""
	app, err := NewApp(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "without-local-agent")
	if err != nil {
		t.Fatal(err)
	}
	defer app.Shutdown(ctx)
	recs, err := app.db.ListAgents(ctx)
	if err != nil || len(recs) != 0 {
		t.Fatalf("关闭本机后旧 token 不得自动创建记录: %+v %v", recs, err)
	}
}
