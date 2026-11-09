package httpx

// runtime.go 是 Hub 启动装配：把 store / auth / agentlink / httpx 串起来。
// 放在 httpx 包外更合适，但当前阶段放在这里可以避免多一个只有三行代码的包。

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/config"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// App 是组装完成的 Hub 运行体。
type App struct {
	cfg      config.Config
	log      *slog.Logger
	db       *store.DB
	registry *agentlink.Registry
	server   *http.Server
	agentSrv *agentlink.Server
	authSvc  *auth.Service
}

// NewApp 构造 Hub。所有依赖在此一次性创建，失败时返回明确原因。
func NewApp(ctx context.Context, cfg config.Config, log *slog.Logger, version string) (*App, error) {
	db, err := store.Open(ctx, cfg.Database.Path)
	if err != nil {
		return nil, err
	}

	authSvc := auth.NewService(db, cfg.Auth.SessionTTL)
	if err := ensureInitialAdmin(ctx, db, cfg, log); err != nil {
		_ = db.Close()
		return nil, err
	}

	reg := agentlink.NewRegistry()

	// 事件处理器：把 Agent 上报的心跳与快照落到数据库。
	helloHandler := &agentSink{db: db, log: log}

	agentSrv := agentlink.NewServer(reg, cfg.Auth.AgentToken, version, helloHandler, log)
	ops := agentlink.NewOps(reg)

	router := NewRouter(Deps{
		Log:        log,
		DB:         db,
		Auth:       authSvc,
		Registry:   reg,
		Ops:        ops,
		AgentConn:  http.HandlerFunc(agentSrv.HandleConnect),
		WebDist:    cfg.Web.Dist,
		HubVersion: version,
	})

	httpSrv := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		// 不设置全局 WriteTimeout：WebSocket 长连接会被它强行切断。
		// 连接存活由 agentlink 的心跳机制负责。
		IdleTimeout: 120 * time.Second,
	}

	return &App{
		cfg:      cfg,
		log:      log,
		db:       db,
		registry: reg,
		server:   httpSrv,
		agentSrv: agentSrv,
		authSvc:  authSvc,
	}, nil
}

// ListenAndServe 启动 HTTP 服务并阻塞。
func (a *App) ListenAndServe() error {
	ln, err := net.Listen("tcp", a.cfg.Server.Addr)
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", a.cfg.Server.Addr, err)
	}
	a.log.Info("Hub 已启动", "addr", ln.Addr().String(), "web_dist", a.cfg.Web.Dist)
	if err := a.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown 优雅关闭：先停止接收新请求，再关闭数据库。
func (a *App) Shutdown(ctx context.Context) error {
	err := a.server.Shutdown(ctx)
	if cerr := a.db.Close(); cerr != nil && err == nil {
		err = cerr
	}
	return err
}

// ensureInitialAdmin 在数据库为空时创建初始管理员账号。
//
// 安全要求：密码绝不硬编码在代码里，必须由配置或环境变量提供；
// 校验已在 config.Validate 中完成（至少 8 位）。
func ensureInitialAdmin(ctx context.Context, db *store.DB, cfg config.Config, log *slog.Logger) error {
	n, err := db.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	hash, err := user.HashPassword(cfg.Auth.InitialPass)
	if err != nil {
		return fmt.Errorf("初始化管理员密码无效: %w", err)
	}
	now := time.Now().UTC()
	u := user.User{
		ID:           "u-" + randomSuffix(),
		Username:     cfg.Auth.InitialUser,
		DisplayName:  "管理员",
		PasswordHash: hash,
		Role:         user.RoleAdmin,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := db.CreateUser(ctx, u); err != nil {
		return fmt.Errorf("创建初始管理员失败: %w", err)
	}
	log.Info("已创建初始管理员账号，请登录后尽快修改密码", "username", u.Username)
	return nil
}
