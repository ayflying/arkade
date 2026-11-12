package httpx

import (
	"arkade/hub/internal/auth"
	"arkade/hub/internal/config"
	accountservice "arkade/hub/internal/service/account"
	oidcservice "arkade/hub/internal/service/oidclogin"
	settingsservice "arkade/hub/internal/service/settings"
	"arkade/hub/internal/store"
	"context"
	"github.com/gogf/gf/v2/errors/gerror"
	"log/slog"
	"os"
)

type identityServices struct {
	settings *settingsservice.Service
	oidc     *oidcservice.Service
	secure   bool
}

func newIdentityServices(ctx context.Context, db *store.DB, authSvc *auth.Service, cfg config.Config, log *slog.Logger) (*identityServices, error) {
	keyPath := cfg.Auth.SettingsKeyFile
	if _, err := os.Lstat(keyPath); os.IsNotExist(err) {
		providers, loadErr := db.ListOIDCProviders(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		for _, provider := range providers {
			if provider.ClientSecretCipher != "" {
				return nil, gerror.New("已存在加密登录配置但主密钥文件缺失，请恢复原密钥，不会自动生成替代密钥")
			}
		}
	}
	// 通知通道同样使用此主密钥；禁止密钥丢失后自动生成导致凭据不可恢复。
	if _, err := os.Lstat(keyPath); os.IsNotExist(err) {
		monitorConfig, loadErr := db.GetMonitorConfig(ctx)
		if loadErr != nil {
			return nil, loadErr
		}
		if monitorConfig.SecretsCiphertext != "" {
			return nil, gerror.New("已存在加密监控通知配置但主密钥缺失，请恢复原密钥")
		}
	}
	key, err := loadSettingsKey(keyPath)
	if err != nil {
		return nil, err
	}
	settings, err := settingsservice.NewWithOptions(db, key, cfg.Server.BaseURL, settingsservice.Options{AllowInsecureLoopback: cfg.Auth.AllowInsecureOIDCLoopback})
	if err != nil {
		return nil, err
	}
	// Read policy before opening the listener; missing/corrupt settings fail closed.
	if _, err := settings.Get(ctx); err != nil {
		return nil, err
	}
	authSvc.SetPolicy(accountservice.New(db).AuthPolicy)
	oidc := oidcservice.New(oidcservice.Deps{DB: db, Auth: authSvc, Audit: oidcAudit(db, log), LoadProvider: func(ctx context.Context, id string) (oidcservice.Credentials, error) {
		value, err := settings.LoadProvider(ctx, id)
		return oidcservice.Credentials{Provider: value.Provider, ClientSecret: value.ClientSecret, RedirectURL: value.RedirectURL, AllowInsecureLoopback: value.AllowInsecureLoopback}, err
	}})
	return &identityServices{settings: settings, oidc: oidc, secure: len(cfg.Server.BaseURL) >= 8 && cfg.Server.BaseURL[:8] == "https://"}, nil
}
