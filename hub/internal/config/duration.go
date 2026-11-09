package config

// duration.go 处理 TOML 中时长字段的可读写法。
//
// Go 的 time.Duration 在 TOML 里用字符串 "24h" 更符合运维习惯。
// 这里提供统一解析入口，配置加载时就把字符串转成 time.Duration，
// 业务代码拿到的永远是已解析的值，不需要每处再解析一次。

import (
	"fmt"
	"time"
)

// ParseDuration 解析时长字符串，空串返回 fallback。
// 单独暴露是为了让配置解析与测试共用同一实现，避免两处写法不一致。
func ParseDuration(raw string, fallback time.Duration) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("时长格式无效(%q)，应形如 30s/15m/72h: %w", raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("时长必须大于 0，实际为 %q", raw)
	}
	return d, nil
}
