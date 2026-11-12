package httpx

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"arkade/hub/internal/auth"
	"arkade/hub/internal/config"
	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
)

func TestIdentityServicesMissingKeyMustNotReplaceEncryptedConfiguration(t *testing.T) {
	ctx := context.Background()
	cfg := config.Default()
	cfg.Database.DSN = testdb.New(t)
	cfg.Auth.SettingsKeyFile = filepath.Join(t.TempDir(), "settings.key")
	db, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider := store.OIDCProvider{ID: "encrypted", DisplayName: "Protected", Issuer: "https://issuer.example", ClientID: "client", ClientSecretCipher: "v1:existing-encrypted-material", Scopes: "openid"}
	if err := db.SaveOIDCProvider(ctx, provider, 0); err != nil {
		t.Fatal(err)
	}
	_, err = newIdentityServices(ctx, db, auth.NewService(db, 0), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err == nil || !strings.Contains(err.Error(), "主密钥文件缺失") {
		t.Fatalf("must require restoring key: %v", err)
	}
	if _, err := os.Stat(cfg.Auth.SettingsKeyFile); !os.IsNotExist(err) {
		t.Fatalf("generated replacement key: %v", err)
	}
}
