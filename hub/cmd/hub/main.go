// Package main 是 Hub 进程入口。
//
// 入口只做三件事：解析参数 → 加载配置 → 交给 httpx.App 运行。
// 所有业务逻辑都在 internal 包里，保证 main 足够薄，便于测试与阅读。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"arkade/hub/internal/config"
	"arkade/hub/internal/httpx"
)

// version 在构建时通过 -ldflags "-X main.version=..." 注入。
var version = "0.1.0-dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("c", "config.toml", "配置文件路径")
		logLevel   = flag.String("log-level", "info", "日志级别: debug/info/warn/error")
		showVer    = flag.Bool("version", false, "打印版本号后退出")
		initCfg    = flag.Bool("init-config", false, "生成配置模板到指定路径后退出")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("arkade-hub", version)
		return nil
	}

	logger := newLogger(resolveLogLevel(*logLevel))

	if *initCfg {
		return writeTemplate(*configPath, logger)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("配置校验失败:\n%w", err)
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	app, err := httpx.NewApp(ctx, cfg, logger, version)
	if err != nil {
		return err
	}

	// 优雅退出：收到 SIGINT/SIGTERM 时给在途请求 15 秒收尾时间。
	errCh := make(chan error, 1)
	go func() { errCh <- app.ListenAndServe() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case sig := <-sigCh:
		logger.Info("收到退出信号，开始优雅关闭", "signal", sig.String())
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer shutdownCancel()
		if err := app.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("关闭时出错: %w", err)
		}
		logger.Info("已安全退出")
		return nil
	}
}

// resolveLogLevel 决定日志级别，优先级：命令行参数 > 环境变量 ARKADE_LOG_LEVEL > 默认 info。
//
// 为什么要读环境变量 -log-level 的默认值是 "info"，无法区分"用户显式写了 -log-level info"
// 和"没写"。这里用 flag 的默认值判断是否被显式指定：未指定时才让环境变量生效，
// 这样容器编排里设 ARKADE_LOG_LEVEL 就能生效，命令行仍可临时压过它。
func resolveLogLevel(flagValue string) string {
	if flagValue != "" && flagValue != "info" {
		return flagValue
	}
	if v := strings.TrimSpace(os.Getenv("ARKADE_LOG_LEVEL")); v != "" {
		return v
	}
	return flagValue
}

// newLogger 构造结构化日志器。
func newLogger(level string) *slog.Logger {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv}))
}

// writeTemplate 写出配置模板，方便首次部署时直接改。
func writeTemplate(path string, log *slog.Logger) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("配置文件已存在，拒绝覆盖: %s", path)
	}
	const tpl = `# Arkade Hub 配置

[server]
addr = "0.0.0.0:8080"
base_url = "http://127.0.0.1:8080"
timeout = "30s"

[database]
# 首期使用 SQLite 单实例，文件所在目录不存在会自动创建
path = "./data/hub.db"

[auth]
# 登录会话有效期
session_ttl = "72h"
# Agent 反向连接使用的预共享 Token，至少 16 位；也可用环境变量 ARKADE_AGENT_TOKEN
agent_token = "请替换为至少16位的随机字符串"
# 首次启动时创建的管理员账号；密码至少 8 位，也可用环境变量 ARKADE_INITIAL_PASS
initial_user = "admin"
initial_pass = "请替换为至少8位的管理员密码"

[web]
# 前端构建产物目录；留空则只提供 API
dist = "./web/dist"
`
	if err := os.WriteFile(path, []byte(tpl), 0o600); err != nil {
		return fmt.Errorf("写入配置模板失败: %w", err)
	}
	log.Info("已生成配置模板，请修改后启动", "path", path)
	return nil
}
