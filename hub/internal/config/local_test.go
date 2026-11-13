package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedLocalRejectsInvalidEnvironmentSwitch(t *testing.T) {
	for _, value := range []string{"flase", "", "endpointsecret"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("ARKADE_LOCAL_ENABLED", value)
			t.Setenv("ARKADE_LOCAL_ENDPOINT", "tcp://endpointsecret@localhost:2375")
			_, err := Load("")
			if err == nil || !strings.Contains(err.Error(), "合法布尔值") {
				t.Fatalf("非法开关必须拒绝加载: %v", err)
			}
			if strings.Contains(err.Error(), "endpointsecret") || strings.Contains(err.Error(), "flase") {
				t.Fatalf("错误回显环境秘密: %v", err)
			}
			cfg := Default()
			cfg.Auth.InitialPass = "test"
			cfg.Local.Enabled = false
			if err := cfg.Validate(); err != nil {
				t.Fatalf("显式配置校验不得读取环境: %v", err)
			}
		})
	}
}

func TestEmbeddedLocalEnabledFieldValidation(t *testing.T) {
	for _, local := range []LocalConfig{{Enabled: true, DataDir: "   ", Runtime: "auto"}, {Enabled: true, DataDir: "valid", Runtime: "endpointsecret"}, {Enabled: true, DataDir: "bad\x00path", Runtime: "docker"}} {
		cfg := Default()
		cfg.Auth.InitialPass = "test"
		cfg.Local = local
		if err := cfg.Validate(); err == nil || strings.Contains(err.Error(), "endpointsecret") {
			t.Fatalf("应安全拒绝非法本机字段: %v", err)
		}
		cfg.Local.Enabled = false
		if err := cfg.Validate(); err != nil {
			t.Fatalf("禁用时不应校验未使用字段: %v", err)
		}
	}
	for _, runtime := range []string{"", "auto", "docker", "podman", "none", " AUTO "} {
		cfg := Default()
		cfg.Auth.InitialPass = "test"
		cfg.Local.Runtime = runtime
		if err := cfg.Validate(); err != nil {
			t.Fatalf("合法运行时 %q: %v", runtime, err)
		}
	}
}

func TestEmbeddedLocalDefaultsAndEnvironment(t *testing.T) {
	cfg := Default()
	if !cfg.Local.Enabled || cfg.Local.DataDir != "/opt/arkade/agent" || cfg.Local.Runtime != "auto" || cfg.Local.Endpoint != "" || cfg.Local.ComposeBin != "" {
		t.Fatalf("defaults %+v", cfg.Local)
	}
	t.Setenv("ARKADE_LOCAL_ENABLED", "false")
	t.Setenv("ARKADE_LOCAL_DATA_DIR", "/test/local")
	t.Setenv("ARKADE_LOCAL_RUNTIME", "podman")
	t.Setenv("ARKADE_LOCAL_ENDPOINT", "unix:///test/podman.sock")
	t.Setenv("ARKADE_LOCAL_COMPOSE_BIN", "/bin/custom-compose")
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Local.Enabled || cfg.Local.DataDir != "/test/local" || cfg.Local.Runtime != "podman" || cfg.Local.Endpoint != "unix:///test/podman.sock" || cfg.Local.ComposeBin != "/bin/custom-compose" {
		t.Fatalf("env %+v", cfg.Local)
	}
}

func TestEmbeddedLocalTOMLAndLegacyToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local.toml")
	if err := os.WriteFile(path, []byte("[local]\nenabled=false\ndata_dir='/toml/agent'\nruntime='docker'\nendpoint='unix:///toml/docker.sock'\ncompose_bin='docker-compose'\n[auth]\ninitial_pass='test'\nlocal_agent_token='old'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Local.Enabled || cfg.Local.DataDir != "/toml/agent" || cfg.Local.Runtime != "docker" || cfg.Local.ComposeBin != "docker-compose" {
		t.Fatalf("toml %+v", cfg.Local)
	}
	if cfg.Auth.LocalAgentToken != "old" {
		t.Fatal("legacy token must still decode")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("deprecated token must not block startup: %v", err)
	}
	cfg.Local.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("enabled embedded local ignores legacy token: %v", err)
	}
}
