package config

// config_test.go 覆盖配置加载与校验的关键边界。
//
// 重点不是测「正常路径能跑」，而是测「配错了会说人话」：
// 静默使用默认值导致改了配置却不生效，是运维排查成本最高的一类问题。

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDefaultValid 验证默认配置除密码外是合法的。
func TestDefaultValid(t *testing.T) {
	cfg := Default()
	cfg.Auth.InitialPass = "TestPass123!"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("默认配置应通过校验，实际: %v", err)
	}
}

// TestValidateRequiresInitialPassword 验证没有初始密码时拒绝启动。
// 代码里绝不允许内置默认密码。
func TestValidateRequiresInitialPassword(t *testing.T) {
	cfg := Default()
	cfg.Auth.InitialPass = ""

	err := cfg.Validate()
	if err == nil {
		t.Fatal("缺少初始管理员密码时必须拒绝启动")
	}
	if !strings.Contains(err.Error(), "initial_pass") && !strings.Contains(err.Error(), "ARKADE_INITIAL_PASS") {
		t.Errorf("错误信息应指明是哪个配置项有问题，实际: %v", err)
	}
}

// 初始密码由部署者指定，短密码不应阻断启动。
func TestValidateAllowsShortInitialPassword(t *testing.T) {
	cfg := Default()
	cfg.Auth.InitialPass = "1"

	if err := cfg.Validate(); err != nil {
		t.Fatalf("短初始密码应允许启动: %v", err)
	}
}

// TestValidateRejectsShortAgentToken 验证过短的 Agent Token 被拒绝。
func TestValidateRejectsShortAgentToken(t *testing.T) {
	cfg := Default()
	cfg.Auth.InitialPass = "TestPass123!"
	cfg.Auth.AgentToken = "short"

	if err := cfg.Validate(); err == nil {
		t.Fatal("过短的 agent_token 必须被拒绝")
	}
}

// TestLoadRejectsUnknownKey 验证拼错的配置项不会被静默忽略。
func TestLoadRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	content := `
[server]
addr = "127.0.0.1:9999"

[auth]
agent_tokne = "typo-field"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("未知配置项必须报错")
	}
	if !strings.Contains(err.Error(), "未知") {
		t.Errorf("错误信息应说明存在未知配置项，实际: %v", err)
	}
}

// TestLoadReadsValidFile 验证正常配置能被完整读取。
func TestLoadReadsValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "good.toml")
	content := `
[server]
addr = "127.0.0.1:12345"
timeout = "10s"

[database]
dsn = "host=localhost user=arkade dbname=contract_test sslmode=disable"

[auth]
session_ttl = "1h"
agent_token = "0123456789abcdef0123"
initial_user = "boss"
initial_pass = "StrongPass123"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写测试配置失败: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if cfg.Server.Addr != "127.0.0.1:12345" {
		t.Errorf("addr 读取错误: %q", cfg.Server.Addr)
	}
	if cfg.Server.Timeout != 10*time.Second {
		t.Errorf("timeout 应解析为 10s，实际 %v", cfg.Server.Timeout)
	}
	if cfg.Auth.SessionTTL != time.Hour {
		t.Errorf("session_ttl 应解析为 1h，实际 %v", cfg.Auth.SessionTTL)
	}
	if cfg.Auth.InitialUser != "boss" {
		t.Errorf("initial_user 读取错误: %q", cfg.Auth.InitialUser)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("合法配置不应校验失败: %v", err)
	}
}

// TestEnsureDirs 验证设置密钥父目录会被自动创建。
func TestEnsureDirs(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")

	cfg := Default()
	cfg.Auth.SettingsKeyFile = filepath.Join(nested, "settings.key")

	if err := cfg.EnsureDirs(); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if info, err := os.Stat(nested); err != nil || !info.IsDir() {
		t.Fatalf("目录应已创建: %s", nested)
	}
}

// TestParseDuration 覆盖时长解析的边界。
func TestParseDuration(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "空串用兜底", raw: "", want: 30 * time.Second},
		{name: "正常小时", raw: "72h", want: 72 * time.Hour},
		{name: "正常秒", raw: "45s", want: 45 * time.Second},
		{name: "非法格式", raw: "一小时", wantErr: true},
		{name: "零值非法", raw: "0s", wantErr: true},
		{name: "负值非法", raw: "-5s", wantErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseDuration(c.raw, 30*time.Second)
			if c.wantErr {
				if err == nil {
					t.Fatalf("应返回错误，实际得到 %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应返回错误: %v", err)
			}
			if got != c.want {
				t.Errorf("解析结果错误: 期望 %v，实际 %v", c.want, got)
			}
		})
	}
}

// TestApplyEnv 覆盖 Hub 侧的环境变量注入路径。
//
// 容器编排里所有可调项都通过 ARKADE_* 透传（见 deploy/docker/docker-compose.yml），
// 少支持一个变量，编排里写了也不生效，且失效是静默的，所以逐个盯死。
func TestApplyEnv(t *testing.T) {
	t.Setenv("ARKADE_AGENT_TOKEN", "env-token-0123456789")
	t.Setenv("ARKADE_INITIAL_PASS", "EnvPass123!")
	t.Setenv("ARKADE_INITIAL_USER", "envadmin")
	t.Setenv("ARKADE_DB_DSN", "postgres://envuser@localhost/envdb?sslmode=disable")
	t.Setenv("ARKADE_LISTEN", "0.0.0.0:9090")
	t.Setenv("ARKADE_BASE_URL", "https://env.example.com")
	t.Setenv("ARKADE_SESSION_TTL", "48h")
	t.Setenv("ARKADE_WEB_DIST", "/tmp/env-web")

	cfg := Config{}
	applyEnv(&cfg)

	if cfg.Auth.AgentToken != "env-token-0123456789" {
		t.Errorf("AgentToken 未生效: %q", cfg.Auth.AgentToken)
	}
	if cfg.Auth.InitialPass != "EnvPass123!" {
		t.Errorf("InitialPass 未生效: %q", cfg.Auth.InitialPass)
	}
	if cfg.Auth.InitialUser != "envadmin" {
		t.Errorf("InitialUser 未生效: %q", cfg.Auth.InitialUser)
	}
	if cfg.Database.DSN != "postgres://envuser@localhost/envdb?sslmode=disable" {
		t.Errorf("DB DSN 未生效: %q", cfg.Database.DSN)
	}
	if cfg.Server.Addr != "0.0.0.0:9090" {
		t.Errorf("监听地址未生效: %q", cfg.Server.Addr)
	}
	if cfg.Server.BaseURL != "https://env.example.com" {
		t.Errorf("BaseURL 未生效: %q", cfg.Server.BaseURL)
	}
	// 48h 这种人类可读写法必须能解析（.env 里写的就是这种）。
	if cfg.Auth.SessionTTL != 48*time.Hour {
		t.Errorf("SessionTTL 未生效: %v", cfg.Auth.SessionTTL)
	}
	if cfg.Web.Dist != "/tmp/env-web" {
		t.Errorf("Web.Dist 未生效: %q", cfg.Web.Dist)
	}
}

func TestDatabaseFieldEnvEscapesCredentialsAndDSNPrecedence(t *testing.T) {
	const password = "p@ss:'\\word /?#%"
	t.Setenv("ARKADE_DB_HOST", "::1")
	t.Setenv("ARKADE_DB_PORT", "15432")
	t.Setenv("ARKADE_DB_USER", "test@user")
	t.Setenv("ARKADE_DB_NAME", "isolated_db")
	t.Setenv("ARKADE_DB_PASSWORD", password)
	t.Setenv("ARKADE_DB_SSLMODE", "require")
	cfg := Default()
	applyEnv(&cfg)
	uri, err := url.Parse(cfg.Database.DSN)
	if err != nil {
		t.Fatal("generated invalid PostgreSQL URL")
	}
	gotPassword, _ := uri.User.Password()
	if uri.Host != "[::1]:15432" || uri.User.Username() != "test@user" || gotPassword != password || uri.Path != "/isolated_db" || uri.Query().Get("sslmode") != "require" {
		t.Fatal("database field environment failed to preserve connection parameters")
	}
	t.Setenv("ARKADE_DB_DSN", "host=explicit dbname=override")
	applyEnv(&cfg)
	if cfg.Database.DSN != "host=explicit dbname=override" {
		t.Fatal("explicit DSN must win over split fields")
	}
}

func TestPostgresAndSettingsKeyConfigurationContracts(t *testing.T) {
	cfg := Default()
	if cfg.Auth.SettingsKeyFile != "/data/settings.key" || strings.Contains(cfg.Database.DSN, "password=") {
		t.Fatal("unsafe defaults")
	}
	cfg.Auth.InitialPass = "Password123!"
	cfg.Database.DSN = " "
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "database.dsn") {
		t.Fatal("empty DSN accepted")
	}
	if err := decodeTOML([]byte("[database]\npath = 'legacy.db'\n"), &cfg); err == nil {
		t.Fatal("legacy SQLite path must not be silently accepted")
	}
	cfg = Default()
	cfg.Auth.InitialPass = "Password123!"
	cfg.Auth.SettingsKeyFile = ""
	if err := cfg.Validate(); err == nil {
		t.Fatal("empty key path accepted")
	}
}

// 会话覆盖仅识别标准 duration 环境变量，不再读取无前缀或秒数别名。
func TestSessionTTLUsesCanonicalEnvironmentOnly(t *testing.T) {
	t.Setenv("ARKADE_SESSION_TTL_SEC", "3600")
	t.Setenv("SESSION_TTL", "1h")
	t.Setenv("ARKADE_SESSION_TTL", "48h")
	cfg := Default()
	applyEnv(&cfg)
	if cfg.Auth.SessionTTL != 48*time.Hour {
		t.Fatalf("标准 duration 配置应独占优先级，实际 %v", cfg.Auth.SessionTTL)
	}
	t.Setenv("ARKADE_SESSION_TTL", "")
	cfg = Default()
	applyEnv(&cfg)
	if cfg.Auth.SessionTTL != 72*time.Hour {
		t.Fatalf("非标准环境变量不应生效: %v", cfg.Auth.SessionTTL)
	}
}
