package httpx

import (
	"arkade/hub/internal/auth"
	"arkade/hub/internal/config"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestMonitorMissingKeyMustNotReplaceCredentials(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = testdb.New(t)
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	db, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.UpdateMonitorConfig(ctx, store.MonitorConfig{ConfigJSON: "{}", SecretsCiphertext: "v1:existing-monitor-credentials"}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = newIdentityServices(ctx, db, auth.NewService(db, 0), cfg, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("监控密钥丢失必须拒绝启动")
	}
	if _, err = os.Stat(cfg.Auth.SettingsKeyFile); !os.IsNotExist(err) {
		t.Fatal("不允许生成替代密钥")
	}
}
