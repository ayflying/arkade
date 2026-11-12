package httpx

import (
	"arkade/hub/internal/config"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeWatcherCloseHookPrecedesStorageAndRetries(t *testing.T) {
	cfg := config.Default()
	cfg.Database.Path = filepath.Join(t.TempDir(), "watchers.db")
	cfg.Auth.InitialPass = "RuntimeWatcherPass123!"
	cfg.Auth.AgentToken = "runtime-watcher-token"
	cfg.Web.Dist = ""
	app, err := NewApp(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), "watcher-test")
	if err != nil {
		t.Fatal(err)
	}
	if len(app.backgroundClose) != 2 {
		t.Fatalf("route controller close hook missing: %d", len(app.backgroundClose))
	}
	original := app.backgroundClose[0]
	attempts := 0
	app.backgroundClose[0] = func(ctx context.Context) error {
		if err := original(ctx); err != nil {
			return err
		}
		if err := app.db.Ping(context.Background()); err != nil {
			t.Fatalf("storage destroyed before watcher join: %v", err)
		}
		attempts++
		if attempts == 1 {
			return context.DeadlineExceeded
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown must return unjoined work error: %v", err)
	}
	select {
	case <-app.stopped:
		t.Fatal("shutdown completed while watcher join failed")
	default:
	}
	if err := app.db.Ping(context.Background()); err != nil {
		t.Fatalf("retry dependencies closed early: %v", err)
	}
	if err := app.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("shutdown did not retry join: %d", attempts)
	}
	select {
	case <-app.stopped:
	default:
		t.Fatal("successful retry did not complete shutdown")
	}
}
