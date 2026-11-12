package config

import "testing"

// Agent 原本已支持空路径环境覆盖，锁定该行为，避免 CLI/镜像去掉 -c 后回归。
func TestLoadEnvOnly(t *testing.T) {
	t.Setenv("ARKADE_AGENT_TOKEN", "env-token")
	t.Setenv("ARKADE_HUB_URL", "ws://hub:8080/agent/connect")
	t.Setenv("ARKADE_DATA_DIR", "")
	t.Setenv("ARKADE_AGENT_NAME", "env-agent")
	t.Setenv("ARKADE_SOCKET", "/var/run/docker.sock")
	t.Setenv("ARKADE_RUNTIME_KIND", "docker")
	t.Setenv("ARKADE_HEARTBEAT_SEC", "30")
	t.Setenv("ARKADE_LOG_LEVEL", "info")
	t.Setenv("ARKADE_LOG_FORMAT", "json")
	t.Setenv("ARKADE_COMPOSE_SOURCE_ROOT", "/")
	t.Setenv("ARKADE_COMPOSE_SOURCE_MOUNT", "/host")

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("纯环境变量配置应通过服务校验: %v", err)
	}
	if cfg.Token != "env-token" || cfg.HubURL != "ws://hub:8080/agent/connect" || cfg.DataDir != "/opt/arkade/agent" || cfg.Name != "env-agent" {
		t.Fatal("连接环境变量或默认数据目录未生效")
	}
	if cfg.Socket != "/var/run/docker.sock" || cfg.RuntimeKind != "docker" || cfg.HeartbeatSec != 30 || cfg.LogLevel != "info" || cfg.LogFormat != "json" || cfg.ComposeSourceRoot != "/" || cfg.ComposeSourceMount != "/host" {
		t.Fatal("运行环境变量未生效")
	}

	t.Setenv("ARKADE_DATA_DIR", "/custom/agent")
	cfg, err = Load("")
	if err != nil || cfg.DataDir != "/custom/agent" {
		t.Fatalf("环境变量数据目录覆盖失败: %v", err)
	}
}
