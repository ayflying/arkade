package config

// config_test.go 覆盖配置加载与校验的关键边界。
//
// 重点不是测「正常路径能跑」，而是测「配错了会说人话」：
// 静默使用默认值导致改了配置却不生效，是运维排查成本最高的一类问题。

import (
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

// TestValidateRejectsShortPassword 验证弱密码被拒绝。
func TestValidateRejectsShortPassword(t *testing.T) {
	cfg := Default()
	cfg.Auth.InitialPass = "short"

	if err := cfg.Validate(); err == nil {
		t.Fatal("过短的密码必须被拒绝")
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
path = "` + filepath.ToSlash(filepath.Join(dir, "d", "hub.db")) + `"

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

// TestEnsureDirs 验证数据库父目录会被自动创建。
func TestEnsureDirs(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c")

	cfg := Default()
	cfg.Database.Path = filepath.Join(nested, "hub.db")

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
