package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

// LocalConfig 配置 Hub 进程内的本机执行端，不经过网络 Agent 凭据认证。
type LocalConfig struct {
	Enabled    bool   `toml:"enabled"`
	DataDir    string `toml:"data_dir"`
	Runtime    string `toml:"runtime"`
	Endpoint   string `toml:"endpoint"`
	ComposeBin string `toml:"compose_bin"`
}

func defaultLocal() LocalConfig {
	return LocalConfig{Enabled: true, DataDir: "/data/local", Runtime: "auto"}
}

// validateLocalEnv 在应用环境覆盖前拒绝拼错的安全开关，不回显环境值。
// Config.Validate 仅检查配置本身，不能再次读取进程环境覆盖显式配置。
func validateLocalEnv() error {
	if v, ok := os.LookupEnv("ARKADE_LOCAL_ENABLED"); ok {
		if _, err := strconv.ParseBool(v); err != nil {
			return errors.New("ARKADE_LOCAL_ENABLED 必须是合法布尔值（true 或 false），拒绝启用本机执行端")
		}
	}
	return nil
}

func (c LocalConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	var errs []error
	if strings.TrimSpace(c.DataDir) == "" || strings.ContainsRune(c.DataDir, '\x00') {
		errs = append(errs, errors.New("启用本机执行端时 local.data_dir 必须是非空有效路径"))
	}
	switch strings.ToLower(strings.TrimSpace(c.Runtime)) {
	case "", "auto", "docker", "podman", "none":
	default:
		errs = append(errs, errors.New("local.runtime 必须是 auto、docker、podman 或 none"))
	}
	return errors.Join(errs...)
}

func applyLocalEnv(cfg *Config) {
	if v, ok := os.LookupEnv("ARKADE_LOCAL_ENABLED"); ok {
		if enabled, err := strconv.ParseBool(v); err == nil {
			cfg.Local.Enabled = enabled
		}
	}
	for key, dest := range map[string]*string{
		"ARKADE_LOCAL_DATA_DIR":    &cfg.Local.DataDir,
		"ARKADE_LOCAL_RUNTIME":     &cfg.Local.Runtime,
		"ARKADE_LOCAL_ENDPOINT":    &cfg.Local.Endpoint,
		"ARKADE_LOCAL_COMPOSE_BIN": &cfg.Local.ComposeBin,
	} {
		if v, ok := os.LookupEnv(key); ok {
			*dest = v
		}
	}
}
