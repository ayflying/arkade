package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTemp 把内容写到临时文件并返回路径。
func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "agent.toml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaults(t *testing.T) {
	// 不指定配置文件时应该拿到一套可用的默认值，
	// 这样 `arkade-agent ps` 才能零配置运行（决策 D-05 的离线可用性要求）。
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("默认配置加载失败: %v", err)
	}
	if cfg.HubURL != DefaultHubURL {
		t.Fatalf("HubURL = %q，期望 %q", cfg.HubURL, DefaultHubURL)
	}
	if cfg.HeartbeatSec != DefaultHeartbeatSec {
		t.Fatalf("HeartbeatSec = %d", cfg.HeartbeatSec)
	}
	if cfg.LogLevel != DefaultLogLevel || cfg.LogFormat != DefaultLogFormat {
		t.Fatalf("日志默认值错误: %+v", cfg)
	}
	if cfg.RuntimeKind != "auto" {
		t.Fatalf("RuntimeKind = %q，期望 auto", cfg.RuntimeKind)
	}
}

func TestLoadValidFile(t *testing.T) {
	p := writeTemp(t, `
hub_url = "wss://hub.example.com:8443/agent/connect"
token = "abc123"
data_dir = "/tmp/arkade-test"
socket = "/var/run/docker.sock"
heartbeat_sec = 20
log_level = "debug"
log_format = "json"
runtime_kind = "podman"
prefer_podman = true
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Token != "abc123" || cfg.HeartbeatSec != 20 || cfg.LogLevel != "debug" {
		t.Fatalf("字段未正确解析: %+v", cfg)
	}
	if cfg.HeartbeatInterval() != 20*time.Second {
		t.Fatalf("心跳间隔换算错误: %v", cfg.HeartbeatInterval())
	}
}

func TestLoadUnknownField(t *testing.T) {
	// 字段名拼错必须报错：静默使用默认值是运维最难排查的一类问题。
	p := writeTemp(t, "huburl = \"ws://x\"\n")
	_, err := Load(p)
	if err == nil {
		t.Fatal("未知字段应报错")
	}
	if !strings.Contains(err.Error(), "huburl") {
		t.Fatalf("错误信息应指出具体字段名，实际: %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err == nil {
		t.Fatal("文件不存在应报错")
	}
	if !strings.Contains(err.Error(), "配置文件不存在") {
		t.Fatalf("错误信息不够人话: %v", err)
	}
}

func TestValidateHubURL(t *testing.T) {
	base := func() Config {
		return Config{HubURL: "ws://127.0.0.1:8080/agent/connect", Token: "t", DataDir: t.TempDir(),
			LogLevel: "info", LogFormat: "text", RuntimeKind: "auto", HeartbeatSec: 15}
	}
	// Validate 是指针方法，这里用临时变量接收以便直接调用。
	ok := base()
	if err := ok.Validate(); err != nil {
		t.Fatalf("合法配置校验失败: %v", err)
	}
	c := base()
	c.HubURL = "http://hub:8080"
	err := c.Validate()
	if err == nil {
		t.Fatal("http:// 应被拒绝并提示改成 ws://")
	}
	if !strings.Contains(err.Error(), "ws://") {
		t.Fatalf("错误信息应给出修复建议，实际: %v", err)
	}

	c = base()
	c.HubURL = "ws://hub:8080"
	if err := c.Validate(); err != nil {
		t.Fatalf("ws:// 应合法: %v", err)
	}

	c = base()
	c.Token = "  "
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("空 token 应报错并指出字段，实际: %v", err)
	}

	c = base()
	c.LogLevel = "verbose"
	if err := c.Validate(); err == nil {
		t.Fatal("非法 log_level 应报错")
	}

	c = base()
	c.HeartbeatSec = 0
	if err := c.Validate(); err == nil {
		t.Fatal("心跳间隔 <=0 应报错")
	}
}

func TestValidateCollectsMultipleProblems(t *testing.T) {
	// 校验要尽量一次报全：让运维一次改完，而不是改一处跑一次。
	c := Config{HubURL: "ftp://x", Token: "", DataDir: "", LogLevel: "x", LogFormat: "y", HeartbeatSec: -1}
	err := c.Validate()
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	for _, want := range []string{"hub_url", "token", "data_dir", "log_level", "log_format", "heartbeat_sec"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息缺少 %q 的问题: %s", want, msg)
		}
	}
}

func TestAgentIDPersistence(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: dir}

	// 首次无 ID。
	id, err := cfg.ResolveAgentID()
	if err != nil {
		t.Fatal(err)
	}
	if id != "" {
		t.Fatalf("首次应返回空 ID，实际 %q", id)
	}

	// 写入后能读回。
	if err := cfg.PersistAgentID("agent-7"); err != nil {
		t.Fatalf("持久化失败: %v", err)
	}
	id, err = cfg.ResolveAgentID()
	if err != nil {
		t.Fatal(err)
	}
	if id != "agent-7" {
		t.Fatalf("读回 ID = %q，期望 agent-7", id)
	}

	// 配置文件里的显式值优先级最高。
	cfg.AgentID = "explicit"
	id, _ = cfg.ResolveAgentID()
	if id != "explicit" {
		t.Fatalf("显式 agent_id 应优先，实际 %q", id)
	}

	// 空 ID 必须拒绝写入，否则下次启动会读到空身份。
	if err := cfg.PersistAgentID(""); err == nil {
		t.Fatal("空 agent_id 应被拒绝")
	}
}

func TestLoadRegistryMissingIsNotError(t *testing.T) {
	cfg := Config{DataDir: t.TempDir()}
	reg, err := cfg.LoadRegistry()
	if err != nil {
		t.Fatalf("文件不存在不应报错: %v", err)
	}
	if reg.Containers == nil {
		t.Fatal("应返回可写的空表")
	}
}

func TestLoadRegistryCorrupt(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: dir}
	if err := os.WriteFile(cfg.RegistryFile(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.LoadRegistry(); err == nil {
		t.Fatal("损坏的登记文件应报错而不是静默返回空表")
	}
}

func TestExpandHome(t *testing.T) {
	// 非 ~ 前缀应原样返回（避免误把相对路径拼到家目录）。
	if got := expandHome("/var/lib/arkade"); got != "/var/lib/arkade" {
		t.Fatalf("普通路径被改写: %q", got)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("无法获取家目录")
	}
	got := expandHome("~/arkade")
	if !strings.HasPrefix(got, home) {
		t.Fatalf("~/ 未展开: %q", got)
	}
}

// TestTokenFromFile 覆盖「配置文件里是否写了凭据」的判定。
//
// 这个判定用于启动时的安全提醒：凭据应当走环境变量注入，
// 写进配置文件会随备份与分发扩散。判定错会造成漏报，所以要有测试盯住。
func TestTokenFromFile(t *testing.T) {
	dir := t.TempDir()

	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("写测试文件失败: %v", err)
		}
		return p
	}

	withToken := write("with.toml", "hub_url = \"ws://h/agent/connect\"\ntoken = \"secret-value-123456\"\n")
	emptyToken := write("empty.toml", "hub_url = \"ws://h/agent/connect\"\ntoken = \"\"\n")
	noToken := write("none.toml", "hub_url = \"ws://h/agent/connect\"\n")
	broken := write("broken.toml", "this is not toml ===\n")

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"写了令牌", withToken, true},
		{"令牌留空", emptyToken, false},
		{"没有令牌字段", noToken, false},
		{"文件不存在", filepath.Join(dir, "missing.toml"), false},
		{"文件格式损坏", broken, false},
		{"路径为空", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TokenFromFile(c.path); got != c.want {
				t.Fatalf("TokenFromFile(%q) = %v，期望 %v", c.path, got, c.want)
			}
		})
	}
}
