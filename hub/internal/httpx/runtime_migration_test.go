package httpx

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"arkade/hub/internal/config"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/testdb"
)

func TestRuntimeWiresMigrationAndClosesBeforeStorage(t *testing.T) {
	cfg := config.Default()
	cfg.Database.DSN = testdb.New(t)
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	cfg.Auth.InitialPass = "RuntimeMigratePass123!"
	cfg.Auth.AgentToken = "runtime-migration-test-token"
	cfg.Web.Dist = ""
	app, err := NewApp(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "migration-runtime-test")
	if err != nil {
		t.Fatal(err)
	}
	if app.migrations == nil {
		t.Fatal("production migration manager is not wired")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = app.migrations.Submit(context.Background(), stackmigrate.Options{Source: "source", Target: "target", StackName: "project", OwnerID: "owner"})
	if err != stackmigrate.ErrClosed {
		t.Fatalf("migration manager accepted work after storage shutdown: %v", err)
	}
}
