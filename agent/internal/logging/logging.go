// Package logging 构造标准库 slog 日志器。
//
// 只做一件事：把配置里的 level/format 变成 *slog.Logger。抽成独立包的好处是
// CLI 与未来其它入口（如升级器）共用同一套日志风格，不会出现半套中文半套英文的日志。
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options 是日志器配置。
type Options struct {
	Level  string    // debug / info / warn / error
	Format string    // text / json
	Writer io.Writer // nil 表示 stderr（服务模式的默认选择）
}

// New 按配置构造日志器。
//
// 选用 text 格式作为默认：这是给运维在 journalctl 里直接看的，JSON 只是给
// 采集器用的可选形态。时间戳使用 slog 默认的 RFC3339，避免自造格式带来的解析摩擦。
func New(opts Options) (*slog.Logger, error) {
	lvl, err := parseLevel(opts.Level)
	if err != nil {
		return nil, err
	}
	w := opts.Writer
	if w == nil {
		w = os.Stderr
	}

	handlerOpts := &slog.HandlerOptions{
		Level: lvl,
		// 源码位置只在 debug 级别带上，避免正常日志里充斥 internal/xxx.go:88 噪声。
		AddSource: lvl <= slog.LevelDebug,
	}

	var h slog.Handler
	switch strings.ToLower(strings.TrimSpace(opts.Format)) {
	case "", "text":
		h = slog.NewTextHandler(w, handlerOpts)
	case "json":
		h = slog.NewJSONHandler(w, handlerOpts)
	default:
		return nil, fmt.Errorf("log_format=%q 无效，可选 text / json", opts.Format)
	}
	return slog.New(h), nil
}

// parseLevel 把配置里的字符串转成 slog 级别。
func parseLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("log_level=%q 无效，可选 debug / info / warn / error", s)
	}
}
