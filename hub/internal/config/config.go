// Package config 负责 Hub 的运行配置加载与校验。
//
// 设计原因：Hub 是单实例服务（决策 D-03），配置来源只有两处——
// 命令行指定的 TOML 文件与少量环境变量覆盖。不引入配置中心，
// 避免控制面为了几十个字段背上额外依赖。
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// DatabaseConfig 是 PostgreSQL 连接配置。不包含文件数据库兼容路径。
type DatabaseConfig struct {
	DSN string `toml:"dsn"` // PostgreSQL URL 或 libpq keyword DSN；不要记录到日志
}

// AuthConfig 是登录与会话配置。
type AuthConfig struct {
	SessionTTL                time.Duration `toml:"session_ttl"`                  // 登录会话有效期
	AgentToken                string        `toml:"agent_token"`                  // 首期 Agent 接入的预共享 Token（简化版）
	InitialUser               string        `toml:"initial_user"`                 // 首次启动时创建的管理员用户名
	InitialPass               string        `toml:"initial_pass"`                 // 首次启动时创建的管理员密码
	SettingsKeyFile           string        `toml:"settings_key_file"`            // OIDC 密钥加密主密钥文件；独立于数据库连接
	AllowInsecureOIDCLoopback bool          `toml:"allow_insecure_oidc_loopback"` // 仅本地开发允许 HTTP loopback OIDC，生产必须关闭
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
		Database: DatabaseConfig{DSN: "host=localhost port=5432 user=arkade dbname=arkade sslmode=disable"},
		Auth: AuthConfig{
			SettingsKeyFile: "./data/settings.key",
			SessionTTL:      72 * time.Hour,
			AgentToken:      "",
			InitialUser:     "admin",
			InitialPass:     "",
		},
		Web: WebConfig{Dist: "./web/dist"},
	}
}

// Load 按默认值 → 可选 TOML → 环境变量的顺序加载配置。
// 显式指定的文件不存在或非法时返回错误，不静默退回默认配置。
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("读取配置文件失败: %w", err)
		}
		if err := decodeTOML(raw, &cfg); err != nil {
			return cfg, fmt.Errorf("解析配置文件失败: %w", err)
		}
	}
	applyEnv(&cfg)
	return cfg, nil
}

// applyEnv 应用环境变量覆盖。
//
// 约定（容器编排统一走这条路）：可配置项通过 ARKADE_* 环境变量覆盖，
// 配置文件提供本地运行默认值。普通参数直接写在编排的 environment 中，
// 仅密码、Token 等凭据引用 .env，不把所有配置间接转入 .env。
func applyEnv(cfg *Config) {
	if v := os.Getenv("ARKADE_AGENT_TOKEN"); v != "" {
		cfg.Auth.AgentToken = v
	}
	if v := os.Getenv("ARKADE_INITIAL_PASS"); v != "" {
		cfg.Auth.InitialPass = v
	}
	if v := os.Getenv("ARKADE_SETTINGS_KEY_FILE"); v != "" {
		cfg.Auth.SettingsKeyFile = v
	}
	if v := os.Getenv("ARKADE_ALLOW_INSECURE_OIDC_LOOPBACK"); v != "" {
		cfg.Auth.AllowInsecureOIDCLoopback = v == "true"
	}
	if v := os.Getenv("ARKADE_INITIAL_USER"); v != "" {
		cfg.Auth.InitialUser = v
	}
	// 完整 DSN 优先；分字段模式由 URL 编码处理密码中的 @、空格、引号等字符。
	if v, ok := os.LookupEnv("ARKADE_DB_DSN"); ok {
		cfg.Database.DSN = v
	} else if dsn, ok := databaseEnvDSN(); ok {
		cfg.Database.DSN = dsn
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

// databaseEnvDSN 的分字段模式使用独立默认值，而不尝试合并另一份完整 DSN。
func databaseEnvDSN() (string, bool) {
	values := map[string]string{"HOST": "localhost", "PORT": "5432", "USER": "arkade", "NAME": "arkade", "PASSWORD": "", "SSLMODE": "disable"}
	present := false
	for field := range values {
		if value, ok := os.LookupEnv("ARKADE_DB_" + field); ok {
			values[field] = value
			present = true
		}
	}
	if !present {
		return "", false
	}
	uri := url.URL{Scheme: "postgres", Host: net.JoinHostPort(values["HOST"], values["PORT"]), Path: "/" + values["NAME"]}
	uri.User = url.User(values["USER"])
	if values["PASSWORD"] != "" {
		uri.User = url.UserPassword(values["USER"], values["PASSWORD"])
	}
	query := url.Values{"sslmode": []string{values["SSLMODE"]}}
	uri.RawQuery = query.Encode()
	return uri.String(), true
}

// Validate 在启动前做完整校验，把配置错误挡在监听端口之前。
func (c Config) Validate() error {
	var errs []error
	if c.Server.Addr == "" {
		errs = append(errs, errors.New("server.addr 不能为空"))
	}
	if strings.TrimSpace(c.Database.DSN) == "" {
		errs = append(errs, errors.New("database.dsn 不能为空"))
	}
	if strings.TrimSpace(c.Auth.SettingsKeyFile) == "" {
		errs = append(errs, errors.New("auth.settings_key_file 不能为空"))
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

// EnsureDirs 仅创建本地设置密钥目录，与远端 PostgreSQL DSN 无关。
// 单独拆出来是为了让启动流程只在一处做文件系统副作用，便于测试。
func (c Config) EnsureDirs() error {
	dir := filepath.Dir(c.Auth.SettingsKeyFile)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("创建设置密钥目录失败: %w", err)
		}
	}
	return nil
}
