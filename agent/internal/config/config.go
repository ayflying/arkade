// Package config 负责 Agent 配置文件的读取、默认值填充与校验。
//
// 设计取舍：
//   - 格式用 TOML：面向运维手写，注释友好，比 JSON 少踩引号与尾逗号的坑；
//   - 解析后统一做「默认值 → 校验」，保证运行期拿到的 Config 一定是可用的，
//     不在业务代码里散落 if 判断；
//   - 校验错误全部是人话，指明字段路径与修复建议（运维不看源码）。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config 是 Agent 的运行期配置。
type Config struct {
	HubURL       string `toml:"hub_url"`       // Hub 的 ws:// 或 wss:// 地址
	Token        string `toml:"token"`         // 注册时由 Hub 下发的访问令牌
	AgentID      string `toml:"agent_id"`      // Hub 分配的 Agent ID，首次连接后落盘
	DataDir      string `toml:"data_dir"`      // 数据目录：persist 的 agent_id、后续编排文件与队列
	Name         string `toml:"name"`          // 服务器显示名，留空则用 os.Hostname()
	Socket       string `toml:"socket"`        // 运行时套接字覆盖，留空则自动发现
	RuntimeKind  string `toml:"runtime_kind"`  // 可选：docker / podman / auto（仅影响探测顺序）
	PreferPodman bool   `toml:"prefer_podman"` // 自动发现时优先 Podman

	HeartbeatSec int    `toml:"heartbeat_sec"` // 与 Hub 协商失败时的兜底心跳间隔（秒）
	LogLevel     string `toml:"log_level"`     // debug / info / warn / error
	LogFormat    string `toml:"log_format"`    // text / json
	InsecureTLS  bool   `toml:"insecure_tls"`  // 跳过 TLS 校验（仅自签名测试环境用）
}

// 默认值集中定义，避免散落在各处的魔法数字。
const (
	DefaultHubURL       = "ws://127.0.0.1:8080/agent/connect"
	DefaultDataDir      = "/opt/arkade/agent"
	DefaultHeartbeatSec = 15
	DefaultLogLevel     = "info"
	DefaultLogFormat    = "text"
)

// Load 从文件读取配置。路径为空时返回默认配置（便于本机 `ps` 这类纯本地命令零配置运行）。
//
// 无论走哪条路径，最后都会应用环境变量覆盖敏感字段（见 applyEnvOverrides）。
func Load(path string) (Config, error) {
	cfg := Config{}
	if strings.TrimSpace(path) == "" {
		cfg.applyDefaults()
		applyEnvOverrides(&cfg)
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("配置文件不存在: %s（用 -c 指定路径，或先参照 deploy/agent.example.toml 创建）", path)
		}
		return Config{}, fmt.Errorf("读取配置文件失败(%s): %w", path, err)
	}

	// 用 Decode 而不是 DecodeFile：上面已经读过内容，且这里要覆盖默认值，
	// 不存在字段保留默认即可（TOML 库对未出现字段不赋值）。
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, fmt.Errorf("解析配置文件失败(%s): %w", path, err)
	}
	// 未知字段直接报错：运维拼错字段名（比如 hub_url 写成 huburl）时必须立刻发现，
	// 否则会静默使用默认值，排查成本极高。
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		return Config{}, fmt.Errorf("配置文件(%s)含有未知字段: %s（请对照 deploy/agent.example.toml）", path, strings.Join(keys, ", "))
	}

	cfg.applyDefaults()
	applyEnvOverrides(&cfg)
	return cfg, nil
}

// applyEnvOverrides 用环境变量覆盖敏感或按机器不同的字段。
//
// 为什么需要：token 属于凭据，明文写进配置文件意味着它会随配置文件一起
// 被备份、被提交、被镜像层缓存。容器与 systemd 部署的通行做法都是走环境变量注入。
//
// 优先级：环境变量 > 配置文件。这样同一份配置可以部署到多台机器，
// 各自的令牌通过环境变量区分。
//
// 必须在 applyDefaults 之后调用（否则空值会被默认值覆盖回），
// 且在 Validate 之前（否则空 token 会被误判为配置错误）。
func applyEnvOverrides(c *Config) {
	if v := strings.TrimSpace(os.Getenv("ARKADE_AGENT_TOKEN")); v != "" {
		c.Token = v
	}
	if v := strings.TrimSpace(os.Getenv("ARKADE_HUB_URL")); v != "" {
		c.HubURL = v
	}
	if v := strings.TrimSpace(os.Getenv("ARKADE_DATA_DIR")); v != "" {
		c.DataDir = v
	}
	if v := strings.TrimSpace(os.Getenv("ARKADE_AGENT_NAME")); v != "" {
		c.Name = v
	}
}

// applyDefaults 填充默认值。数据目录还要展开 ~ 与环境变量，方便运维手写。
func (c *Config) applyDefaults() {
	if strings.TrimSpace(c.HubURL) == "" {
		c.HubURL = DefaultHubURL
	}
	if strings.TrimSpace(c.DataDir) == "" {
		c.DataDir = DefaultDataDir
	}
	c.DataDir = expandHome(c.DataDir)
	if c.HeartbeatSec <= 0 {
		c.HeartbeatSec = DefaultHeartbeatSec
	}
	if c.LogLevel == "" {
		c.LogLevel = DefaultLogLevel
	}
	if c.LogFormat == "" {
		c.LogFormat = DefaultLogFormat
	}
	c.RuntimeKind = strings.ToLower(strings.TrimSpace(c.RuntimeKind))
	if c.RuntimeKind == "" {
		c.RuntimeKind = "auto"
	}
}

// expandHome 展开 ~ 与 $VAR 形式。仅处理最常见的 ~/ 前缀，不做完整 shell 展开。
func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if strings.HasPrefix(p, "~/") || p == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, p[2:])
		}
	}
	return os.ExpandEnv(p)
}

// Validate 校验服务模式所需的配置。
//
// 为什么单独拆出来而不是塞进 Load：本地 CLI（ps/start/stop/logs）不需要 hub_url
// 与 token，把"必须联网"的校验绑死在读取阶段会让离线命令无法使用——这与决策 D-05
// 的离线自治目标直接冲突。
func (c *Config) Validate() error {
	var problems []string

	if err := validateHubURL(c.HubURL); err != nil {
		problems = append(problems, err.Error())
	}
	if strings.TrimSpace(c.Token) == "" {
		problems = append(problems, "token 不能为空：请在 Hub 的「Agent 注册」页面创建 Agent 后复制令牌")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		problems = append(problems, "data_dir 不能为空：需要一个可写目录保存 agent_id 与后续编排文件")
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		problems = append(problems, fmt.Sprintf("log_level=%q 无效，可选 debug / info / warn / error", c.LogLevel))
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		problems = append(problems, fmt.Sprintf("log_format=%q 无效，可选 text / json", c.LogFormat))
	}
	switch c.RuntimeKind {
	case "auto", "docker", "podman":
	default:
		problems = append(problems, fmt.Sprintf("runtime_kind=%q 无效，可选 auto / docker / podman", c.RuntimeKind))
	}
	if c.HeartbeatSec <= 0 || c.HeartbeatSec > 300 {
		problems = append(problems, fmt.Sprintf("heartbeat_sec=%d 不合理，应在 1~300 秒之间", c.HeartbeatSec))
	}

	if len(problems) > 0 {
		return fmt.Errorf("配置校验未通过:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// validateHubURL 校验 Hub 地址。
//
// 明确要求 ws:// 或 wss://：http:// 虽然能被"猜"成 ws://，但猜错时排障成本很高
// （表现为握手失败而不是配置报错），所以直接拒绝并给出改法。
func validateHubURL(raw string) error {
	s := strings.TrimSpace(raw)
	if s == "" {
		return errors.New("hub_url 不能为空：形如 ws://hub.example.com:8080/agent/connect 或 wss://...")
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("hub_url 格式非法(%s): %w", s, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
	case "http", "https":
		return fmt.Errorf("hub_url 协议应为 ws:// 或 wss://，当前为 %s://（把 http 换成 ws、https 换成 wss 即可）", u.Scheme)
	default:
		return fmt.Errorf("hub_url 协议不支持: %q（只支持 ws:// 与 wss://）", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("hub_url 缺少主机名: %s", s)
	}
	return nil
}

// HeartbeatInterval 把秒配置转成 time.Duration。
func (c *Config) HeartbeatInterval() time.Duration {
	sec := c.HeartbeatSec
	if sec <= 0 {
		sec = DefaultHeartbeatSec
	}
	return time.Duration(sec) * time.Second
}
