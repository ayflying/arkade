package httpx

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"arkade/hub/internal/auth"
	"arkade/hub/internal/config"
	"arkade/hub/internal/store"
)

func testIdentityServices(t *testing.T, db *store.DB, svc *auth.Service, log *slog.Logger) *identityServices {
	t.Helper()
	cfg := config.Default()
	cfg.Local.Enabled = false
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	cfg.Server.BaseURL = "http://127.0.0.1:8080"
	identity, err := newIdentityServices(context.Background(), db, svc, cfg, log)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}
