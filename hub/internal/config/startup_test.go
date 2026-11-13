package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadEnvOnlyAndFileOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hub.toml")
	if err := os.WriteFile(path, []byte("[server]\naddr = \"127.0.0.1:1234\"\ntimeout = \"10s\"\n[auth]\ninitial_pass = \"FilePass123\"\n[database]\ndsn = \"host=localhost dbname=file_db\"\n[web]\ndist = \"file-dist\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKADE_INITIAL_PASS", "EnvPass123!")
	t.Setenv("ARKADE_INITIAL_USER", "env-admin")
	t.Setenv("ARKADE_AGENT_TOKEN", "env-token-0123456789")
	t.Setenv("ARKADE_DB_DSN", "host=postgres user=arkade dbname=arkade sslmode=disable")
	t.Setenv("ARKADE_WEB_DIST", "/app/web/dist")
	t.Setenv("ARKADE_LISTEN", "0.0.0.0:9090")
	t.Setenv("ARKADE_BASE_URL", "https://hub.example.com")
	t.Setenv("ARKADE_SESSION_TTL", "48h")
	t.Setenv("ARKADE_SETTINGS_KEY_FILE", "/data/settings.key")
	t.Setenv("ARKADE_ALLOW_INSECURE_OIDC_LOOPBACK", "false")

	for _, p := range []string{"", path} {
		t.Run(p, func(t *testing.T) {
			cfg, err := Load(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("环境变量配置应通过校验: %v", err)
			}
			if cfg.Auth.InitialPass != "EnvPass123!" || cfg.Auth.InitialUser != "env-admin" || cfg.Auth.AgentToken != "env-token-0123456789" || cfg.Auth.SessionTTL != 48*time.Hour {
				t.Fatal("认证环境变量未生效")
			}
			if cfg.Database.DSN != "host=postgres user=arkade dbname=arkade sslmode=disable" || cfg.Web.Dist != "/app/web/dist" || cfg.Server.Addr != "0.0.0.0:9090" || cfg.Server.BaseURL != "https://hub.example.com" || cfg.Auth.SettingsKeyFile != "/data/settings.key" || cfg.Auth.AllowInsecureOIDCLoopback {
				t.Fatal("容器环境变量未生效")
			}
			wantTimeout := 30 * time.Second
			if p != "" {
				wantTimeout = 10 * time.Second
			}
			if cfg.Server.Timeout != wantTimeout {
				t.Fatalf("未覆盖字段的默认值/TOML 值丢失: %v", cfg.Server.Timeout)
			}
		})
	}
}

func TestLoadExplicitMissingFileFailsWithEnv(t *testing.T) {
	t.Setenv("ARKADE_INITIAL_PASS", "EnvPass123!")
	if _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err == nil {
		t.Fatal("显式指定不存在的 TOML 不能静默使用环境变量启动")
	}
}
