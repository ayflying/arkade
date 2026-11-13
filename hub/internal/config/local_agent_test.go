package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalAgentTokenValidation(t *testing.T) {
	for _, tc := range []struct {
		name, local, shared string
		invalid             bool
	}{
		{"可选", "", "", false},
		{"旧短密钥被忽略", "short", "", false},
		{"十六位边界", "0123456789abcdef", "", false},
		{"旧共享密钥被忽略", "0123456789abcdef", "0123456789abcdef", false},
		{"独立密钥", "local-token-012345", "shared-token-012345", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			cfg.Auth.InitialPass = "test"
			cfg.Auth.LocalAgentToken, cfg.Auth.AgentToken = tc.local, tc.shared
			err := cfg.Validate()
			if (err != nil) != tc.invalid {
				t.Fatalf("校验结果错误: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), tc.local) {
				t.Fatal("错误不能泄漏密钥")
			}
		})
	}
}

func TestLoadLocalAgentToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local.toml")
	if err := os.WriteFile(path, []byte("[auth]\nlocal_agent_token = 'toml-token-0123456'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARKADE_LOCAL_AGENT_TOKEN", "")
	cfg, err := Load(path)
	if err != nil || cfg.Auth.LocalAgentToken != "toml-token-0123456" {
		t.Fatalf("TOML 加载失败: %v", err)
	}
	t.Setenv("ARKADE_LOCAL_AGENT_TOKEN", "env-local-token-0123456")
	cfg, err = Load(path)
	if err != nil || cfg.Auth.LocalAgentToken != "env-local-token-0123456" {
		t.Fatalf("环境变量覆盖失败: %v", err)
	}
}
