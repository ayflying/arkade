package httpx

// runtime.go 是 Hub 启动装配：把 store / auth / agentlink / httpx 串起来。
// 放在 httpx 包外更合适，但当前阶段放在这里可以避免多一个只有三行代码的包。

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"arkade/agent/embedded"
	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/config"
	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/monitor"
	"arkade/hub/internal/service/auditretention"
	"arkade/hub/internal/service/stackmigration"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
)

// App 是组装完成的 Hub 运行体。
type App struct {
	cfg             config.Config
	log             *slog.Logger
	db              *store.DB
	registry        *agentlink.Registry
	connections     *connectionTracker
	server          *ghttp.Server
	stopped         chan struct{}
	stopOnce        sync.Once
	agentSrv        *agentlink.Server
	authSvc         *auth.Service
	imageTasks      *imagetask.Manager
	migrations      *stackmigrate.Manager
	autoAdopt       *autoAdopter
	backgroundClose []func(context.Context) error
	localExecutor   *embedded.Executor
}

// NewApp 构造 Hub。所有依赖在此一次性创建，失败时返回明确原因。
func NewApp(ctx context.Context, cfg config.Config, log *slog.Logger, version string) (*App, error) {
	db, err := store.Open(ctx, cfg.Database.DSN)
	if err != nil {
		return nil, err
	}

	// 内置本机不使用任何接入令牌；旧部署变量仅提示，不再创建外部连接凭据。
	if cfg.Auth.LocalAgentToken != "" {
		log.Warn("ARKADE_LOCAL_AGENT_TOKEN 已弃用，Hub内置本机无需令牌")
	}

	authSvc := auth.NewService(db, cfg.Auth.SessionTTL)
	identityServices, err := newIdentityServices(ctx, db, authSvc, cfg, log)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := ensureInitialAdmin(ctx, db, cfg, log); err != nil {
		_ = db.Close()
		return nil, err
	}

	reg := agentlink.NewRegistry()
	// 保留身份在开放握手前完成，关闭本机执行也不允许远程冒名接管。
	if err := reg.ReserveLocalID(store.LocalAgentID); err != nil {
		_ = db.Close()
		return nil, err
	}

	// 事件处理器：把 Agent 上报的心跳与快照落到数据库。
	helloHandler := &agentSink{db: db, log: log}

	agentSrv := agentlink.NewServer(reg, cfg.Auth.AgentToken, version, helloHandler, log)
	// 注入专属凭据查询：有了它，专属密钥才能按摘要解析出绑定身份，
	// 共享 token 也才能拒绝认领已被专属密钥绑定的服务器。
	agentSrv.SetCredentialStore(NewCredentialStore(db))
	// 注入握手临界区守卫：握手侧与撤销侧（reset/delete/disable）共用同一把锁，
	// 消除"认证通过后、注册前被撤销"的竞态。撤销侧拿的就是这个对象。
	guard := agentlink.NewEnrollmentGuard()
	agentSrv.SetEnrollmentGuard(guard)
	ops := agentlink.NewOps(reg)

	imageBackend := agentlink.NewImageBackend(ops)
	imageTasks := imagetask.New(imageBackend)
	migrations := stackmigrate.New(stackmigration.New(stackmigration.Deps{
		DB: db, Ops: ops, Registry: reg, Images: imageBackend,
	}))
	// 监控复用独立持久化主密钥，不保存明文通道凭据。
	monitorKey, err := loadSettingsKey(cfg.Auth.SettingsKeyFile)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	monitorService, err := monitor.NewService(db, monitorKey, nil)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := monitorService.Get(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	localExecutor, err := newLocalExecutor(ctx, cfg, db, reg, log, version)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	monitorEngine := monitor.NewEngine(db, &monitorSource{db: db, registry: reg}, monitorService.Config, monitorService.Notify, log)
	autoAdopt := newAutoAdopter(db, ops, reg.OnlineIDs)
	connections := &connectionTracker{}
	var backgroundClose []func(context.Context) error
	router := NewRouter(Deps{
		RegisterBackgroundClose: func(close func(context.Context) error) { backgroundClose = append(backgroundClose, close) },
		MonitorService:          monitorService,
		MonitorEngine:           monitorEngine,
		AutoAdopt:               autoAdopt,
		ImageTasks:              imageTasks,
		StackMigrate:            migrations,
		Log:                     log,
		DB:                      db,
		Auth:                    authSvc,
		Identity:                identityServices,
		Registry:                reg,
		Ops:                     ops,
		AgentConn:               connections.wrap(http.HandlerFunc(agentSrv.HandleConnect)),
		WebDist:                 cfg.Web.Dist,
		HubVersion:              version,
		// 传给 handler 只为"生成密钥时避开与共享 token 同值"。
		// 它不会写库、不会进响应、不会进日志。
		AgentSharedToken: cfg.Auth.AgentToken,
		Guard:            guard,
	})

	retention := auditretention.New(context.Background(), db, log)
	backgroundClose = append(backgroundClose, retention.Close)
	monitorEngine.Start(context.Background())
	backgroundClose = append(backgroundClose, monitorEngine.Shutdown)
	backgroundClose = append(backgroundClose, startMonitorRetention(db, log))
	if localExecutor != nil {
		backgroundClose = append(backgroundClose, startLocalHeartbeat(db, localExecutor, log, version))
	}

	// GoFrame 的 session/request 清理依赖 Start 初始化，不能只把未启动
	// 的 Server 塞进另一个 http.Server；生产监听与关闭都使用框架生命周期。
	httpSrv := router
	httpSrv.SetAddr(cfg.Server.Addr)
	httpSrv.SetReadTimeout(0)
	httpSrv.SetWriteTimeout(0) // WebSocket/日志流不设置全局写超时。
	httpSrv.SetIdleTimeout(120 * time.Second)
	httpSrv.SetGraceful(false) // 信号仍由 cmd/hub 统一管理，不启用热重载。
	httpSrv.SetGracefulShutdownTimeout(15)
	httpSrv.SetSessionCookieOutput(false) // 登录仅使用现有数据库会话 Cookie。

	return &App{
		localExecutor:   localExecutor,
		cfg:             cfg,
		log:             log,
		db:              db,
		registry:        reg,
		connections:     connections,
		server:          httpSrv,
		stopped:         make(chan struct{}),
		agentSrv:        agentSrv,
		authSvc:         authSvc,
		imageTasks:      imageTasks,
		migrations:      migrations,
		autoAdopt:       autoAdopt,
		backgroundClose: backgroundClose,
	}, nil
}

// ListenAndServe 启动 HTTP 服务并阻塞。
func (a *App) ListenAndServe() error {
	if err := a.server.Start(); err != nil {
		return fmt.Errorf("监听 %s 失败: %w", a.cfg.Server.Addr, err)
	}
	a.autoAdopt.start(context.Background())
	a.log.Info("Hub 已启动", "addr", a.server.GetListenedAddress(), "web_dist", a.cfg.Web.Dist)
	<-a.stopped
	return nil
}

// Shutdown 优雅关闭：先停止接收新请求，再关闭数据库。
func (a *App) Shutdown(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		seconds := int(time.Until(deadline).Seconds())
		if seconds < 1 {
			seconds = 1
		}
		a.server.SetGracefulShutdownTimeout(seconds)
	}
	// 框架 Shutdown 使用配置的优雅退出期限，不接受 context 参数。
	// 先停止监听并等待框架的请求排空期限，再关闭后台任务和业务池。
	// 避免退出期间仍接受请求、却已经销毁请求依赖的任务管理器。
	err := a.server.Shutdown()
	// Route-owned compose watchers can still finalize database rows and call
	// Agent RPC. Join them after HTTP drains, before destroying either dependency.
	for _, close := range a.backgroundClose {
		if cerr := close(ctx); cerr != nil {
			return cerr
		}
	}
	// 后台迁移仍可能使用 Agent RPC 和数据库；先取消并等待资源释放，
	// 再断开 Agent，避免中断导入后连清理临时归档的通道也先被关闭。
	if a.migrations != nil {
		if cerr := a.migrations.Close(ctx); cerr != nil {
			return cerr // 工作未排空时保留连接与业务池，不能提前销毁依赖。
		}
	}
	if a.connections != nil {
		joinCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if cerr := a.connections.close(joinCtx, a.registry); cerr != nil {
			// A live handler can still access storage: do not close its database.
			return cerr
		}
	}
	if a.autoAdopt != nil {
		a.autoAdopt.close()
	}
	if a.imageTasks != nil {
		if cerr := a.imageTasks.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}
	if a.localExecutor != nil {
		a.registry.RemoveLocal(store.LocalAgentID)
		if cerr := a.localExecutor.Close(ctx); cerr != nil {
			return cerr
		}
	}
	if cerr := a.db.Close(); cerr != nil && err == nil {
		err = cerr
	}
	a.stopOnce.Do(func() { close(a.stopped) })
	return err
}

// ensureInitialAdmin 在数据库为空时创建初始管理员账号。
//
// 安全要求：密码绝不硬编码在代码里，必须由配置或环境变量提供；
// 初始密码允许短密码，非空白与 bcrypt 上限由 HashInitialPassword 校验。
func ensureInitialAdmin(ctx context.Context, db *store.DB, cfg config.Config, log *slog.Logger) error {
	n, err := db.CountUsers(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	hash, err := user.HashInitialPassword(cfg.Auth.InitialPass)
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
