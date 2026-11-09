// Package config 负责 Hub 的运行配置加载与校验。
//
// 设计原因：Hub 是单实例服务（决策 D-03），配置来源只有两处——
// 命令行指定的 TOML 文件与少量环境变量覆盖。不引入配置中心，
// 避免控制面为了几十个字段背上额外依赖。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Config 是 Hub 的完整运行配置。
type Config struct {
	Server   ServerConfig   `toml:"server"`
	Database DatabaseConfig `toml:"database"`
	Auth     AuthConfig     `toml:"auth"`
	Web      WebConfig      `toml:"web"`
}

// ServerConfig 是 HTTP 服务监听配置。
type ServerConfig struct {
	Addr    string        `toml:"addr"`     // 监听地址，如 0.0.0.0:8080
	BaseURL string        `toml:"base_url"` // 对外地址，用于生成 Agent 连接说明
	Timeout time.Duration `toml:"timeout"`  // 单请求读写超时
}

// DatabaseConfig 是 SQLite 存储配置。
type DatabaseConfig struct {
	Path string `toml:"path"` // *.db 文件路径，父目录不存在会自动创建
}

// AuthConfig 是登录与会话配置。
type AuthConfig struct {
	SessionTTL  time.Duration `toml:"session_ttl"`  // 登录会话有效期
	AgentToken  string        `toml:"agent_token"`  // 首期 Agent 接入的预共享 Token（简化版）
	InitialUser string        `toml:"initial_user"` // 首次启动时创建的管理员用户名
	InitialPass string        `toml:"initial_pass"` // 首次启动时创建的管理员密码
}

// WebConfig 是前端静态资源托管配置。
type WebConfig struct {
	Dist string `toml:"dist"` // 前端构建产物目录；为空则不托管前端，仅提供 API
}

// Default 返回可直接运行的默认配置，供 `hub -init-config` 生成模板使用。
func Default() Config {
	return Config{
		Server: ServerConfig{
			Addr:    "0.0.0.0:8080",
			BaseURL: "http://127.0.0.1:8080",
			Timeout: 30 * time.Second,
		},
		Database: DatabaseConfig{Path: "./data/hub.db"},
		Auth: AuthConfig{
			SessionTTL:  72 * time.Hour,
			AgentToken:  "",
			InitialUser: "admin",
			InitialPass: "",
		},
		Web: WebConfig{Dist: "./web/dist"},
	}
}

// Load 从指定文件加载配置。文件不存在时返回明确错误，不做静默兜底——
// 静默使用默认配置导致「改了配置却没生效」是排查成本极高的问题。
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		return cfg, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("读取配置文件失败: %w", err)
	}
	if err := decodeTOML(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("解析配置文件失败: %w", err)
	}
	applyEnv(&cfg)
	return cfg, nil
}

// applyEnv 应用环境变量覆盖。
//
// 约定（容器编排统一走这条路）：所有可配置项都能用 ARKADE_* 环境变量覆盖，
// 配置文件只提供本地运行的默认值。编排文件里用 ${VAR} 把这些变量透传进来，
// 换环境只改 .env，不必改配置或重建镜像。
func applyEnv(cfg *Config) {
	if v := os.Getenv("ARKADE_AGENT_TOKEN"); v != "" {
		cfg.Auth.AgentToken = v
	}
	if v := os.Getenv("ARKADE_INITIAL_PASS"); v != "" {
		cfg.Auth.InitialPass = v
	}
	if v := os.Getenv("ARKADE_INITIAL_USER"); v != "" {
		cfg.Auth.InitialUser = v
	}
	if v := os.Getenv("ARKADE_DB_PATH"); v != "" {
		cfg.Database.Path = v
	}
	if v := os.Getenv("ARKADE_LISTEN"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("ARKADE_BASE_URL"); v != "" {
		cfg.Server.BaseURL = v
	}
	// 会话有效期同时支持两种写法：
	//   ARKADE_SESSION_TTL=72h   人类可读，推荐；
	//   ARKADE_SESSION_TTL_SEC=0 纯秒数，保留兼容旧脚本。
	if v := os.Getenv("ARKADE_SESSION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Auth.SessionTTL = d
		}
	}
	if v := os.Getenv("ARKADE_SESSION_TTL_SEC"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Auth.SessionTTL = time.Duration(n) * time.Second
		}
	}
	if v := os.Getenv("ARKADE_WEB_DIST"); v != "" {
		cfg.Web.Dist = v
	}
}

// Validate 在启动前做完整校验，把配置错误挡在监听端口之前。
func (c Config) Validate() error {
	var errs []error
	if c.Server.Addr == "" {
		errs = append(errs, errors.New("server.addr 不能为空"))
	}
	if c.Database.Path == "" {
		errs = append(errs, errors.New("database.path 不能为空"))
	}
	if c.Auth.SessionTTL <= 0 {
		errs = append(errs, errors.New("auth.session_ttl 必须大于 0"))
	}
	if c.Auth.InitialPass == "" {
		errs = append(errs, errors.New("首次启动必须通过 auth.initial_pass 或环境变量 ARKADE_INITIAL_PASS 设置管理员密码"))
	}
	if len(c.Auth.InitialPass) > 0 && len(c.Auth.InitialPass) < 8 {
		errs = append(errs, errors.New("管理员密码至少 8 位"))
	}
	if len(c.Auth.AgentToken) > 0 && len(c.Auth.AgentToken) < 16 {
		errs = append(errs, errors.New("agent_token 至少 16 位，过短容易被爆破"))
	}
	return errors.Join(errs...)
}

// EnsureDirs 创建配置里涉及的目录（数据库父目录、日志目录等）。
// 单独拆出来是为了让启动流程只在一处做文件系统副作用，便于测试。
func (c Config) EnsureDirs() error {
	dir := filepath.Dir(c.Database.Path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("创建数据库目录失败: %w", err)
		}
	}
	return nil
}
