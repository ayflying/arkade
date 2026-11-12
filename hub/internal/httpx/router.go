package httpx

// router.go 装配全部路由。
//
// 路由分层：
//   /agent/connect        Agent 反向连接（Bearer Token 认证，不经过会话中间件）
//   /api/auth/*           登录相关（login 不需要会话，logout/me 需要）
//   /api/*               业务接口（必须经过会话认证）
//   其余 GET             前端 SPA 回落（history 模式必需）
//
// 所有业务及 Agent 连接入口均由 API g.Meta + 原生 typed Bind 注册。
// 仅静态资源/404 兜底使用 BindHandler，不再桥接旧业务 handler。
// 普通 JSON 返回裸响应；上传下载和 WebSocket 显式保留传输控制。
//
// ## 中间件为什么不用 s.Use（重要）
//
// 迁移前的中间件链是**标准库**中间件（recoverer / requestLogger /
// securityHeaders / authenticator / auditMiddleware），它们靠包装
// http.ResponseWriter 来工作——最典型的是 requestLogger 与 auditMiddleware
// 里的 statusWriter，需要拦下 WriteHeader 才能记录状态码与响应大小。
//
// GF 的全局中间件（s.Use）接收的是 `func(*ghttp.Request)`，它内部的
// `r.Middleware.Next()` 会让 GF 用自己的 *response.Writer 继续往下走，
// **中间件里包出来的 writer 会被丢掉**。结果是状态码/大小日志失真，
// WebSocket 的 Hijacker 也可能在包装层断裂。
//
// 所以这里不挂 GF 全局中间件，而是**把标准库中间件链整体塞进每个路由**：
//
//	gfChainWithSanitizer(..., recoverer, requestLogger, securityHeaders, ...)
//
// 整条链始终作用于同一个 `r.Response.Writer`，writer 一路透传，
// 状态码、响应大小、Hijacker 全部正常。代价是每条路由各包一遍，
// 但这只是闭包构造，不涉及运行时开销。
//
// 兜底路由同样按这个方式包一遍（见 fallbackHandler）。

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"

	authapi "arkade/hub/api/auth/v1"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/imagetask"
	"arkade/hub/internal/stackmigrate"
	"arkade/hub/internal/store"
)

// Deps 是路由装配所需依赖，集中传入便于测试替换。
type Deps struct {
	AutoAdopt  *autoAdopter
	ImageTasks *imagetask.Manager
	Log        *slog.Logger
	DB         *store.DB
	Auth       *auth.Service
	Identity   *identityServices
	Registry   *agentlink.Registry
	Ops        *agentlink.Ops
	// StackMigrate 是跨服务器迁移编排器。
	//
	// 为 nil 时不注册 migrate 路由（返回 500 说明"未启用"，
	// 而不是 404——404 会被前端理解成「后端尚未部署」）。
	// 这样既能让测试按需省略它，又不会把"没装配"伪装成"没部署"。
	StackMigrate *stackmigrate.Manager
	AgentConn    http.Handler // Agent 连接处理器（不套会话中间件）
	WebDist      string       // 前端产物目录，空则不托管
	HubVersion   string
	// AgentSharedToken 是配置里的全局共享 token 明文。
	//
	// 只在生成专属密钥时用于"避免同值"检查，不会写库、不会出现在任何响应里。
	// 之所以需要它：若一把专属密钥恰好与共享 token 同值，
	// 认证时同一个字符串会有两种解释（专属身份 vs 共享身份），
	// 那是提权漏洞的经典形态。生成时避开它，比事后加判断干净得多。
	AgentSharedToken string
	// Guard 把"握手最终校验 + 写库 + 登记"与"撤销写库 + 断开"串行化。
	//
	// 撤销类 handler（reset / delete / disable）必须用它包住写库与 Close，
	// 否则会与在途握手产生竞态：那次握手的 Close 找不到连接，
	// 而它会照常上线甚至复活已删记录（详见 agentlink/generation.go）。
	Guard *agentlink.EnrollmentGuard
	// RegisterBackgroundClose attaches route-owned workers to the App lifecycle.
	// Called during router construction, never concurrently with serving requests.
	RegisterBackgroundClose func(func(context.Context) error)
}

// globalChain 把标准库全局中间件依次包在最外层。
//
// 顺序：recoverer → requestLogger → securityHeaders → 内层。
//
//   - recoverer 必须最外层：能兜住日志与业务里的 panic；
//   - securityHeaders 在内侧：保证错误响应也带安全头；
//   - requestLogger 中间：它记录的应该包含安全头设置在内的完整处理过程。
//
// 返回值的类型是 http.Handler，最终由 wrapToGF 桥接给 ghttp。
func globalChain(h http.Handler, log *slog.Logger) http.Handler {
	return recoverer(log)(requestLogger(log)(securityHeaders(h)))
}

// NewRouter 构造根路由，返回 **GoFrame 的 ghttp.Server**。
//
// 返回具体类型（而不是 http.Handler）的原因：
// 生产装配需要调用 SetAddr / SetReadTimeout / Start / Shutdown 等
// 框架方法建立完整生命周期。`*ghttp.Server` 本身也实现 http.Handler，
// 需要 Handler 接口的场合可直接传入，兼容性不变。
//
// ## 实例名必须唯一（实测结论）
//
// ghttp 实例经全局注册表管理，**同名必然复用同一实例**，且没有删除接口。
// 若每次 NewRouter 都用固定名字，测试之间会共享路由表，而重复注册
// 同一 route 会走 `Logger().Fatalf` **直接终止进程**——
// 表现为 `go test` 无输出 FAIL，极难排查。
// 因此这里用进程内自增序号生成唯一名字，避免多个路由实例相互干扰。
func NewRouter(d Deps) *ghttp.Server {
	if d.ImageTasks == nil {
		d.ImageTasks = imagetask.New(agentlink.NewImageBackend(d.Ops))
	}

	// Configuration is set explicitly below; avoid global gins config/view discovery.
	s := ghttp.GetServer(uniqueServerName())
	// 关掉路由表打印：它是 GF 启动时的调试输出，测试里会刷屏。
	s.SetDumpRouterMap(false)

	// 公开路由的中间件链：只有全局三层。
	// 私有路由在此之上再套 authenticator → auditMiddleware（见下）。
	// 注意：这里构造的是标准库链，不是 GF 的 s.Use。

	// ---------- 公开路由 ----------

	// 健康检查：用于容器探活与部署校验，不需要认证。
	//
	// **真正的 GoFrame 规范路由绑定**：
	//   - 路由由 api/health/v1.HealthReq 的 g.Meta 声明；
	//   - group.Bind 由框架完成路由注册与请求绑定（不自建解析）；
	//   - 响应由 bareJSONMiddleware 按 Hub 裸 JSON 契约写出，
	//     替换掉默认的 MiddlewareHandlerResponse 策略。
	healthCtl := controller.NewHealth(controller.HealthDeps{
		Ping:         d.DB.Ping,
		AgentsOnline: d.Registry.Count,
		Version:      d.HubVersion,
	})
	s.Group("/", func(g *ghttp.RouterGroup) {
		// 一条链解决全部三件事：
		//   ① 全局三层（recoverer → requestLogger → securityHeaders）
		//   ② 响应序列化（bareJSON 在最内层，写入仍在 statusWriter 生效期）
		//   ③ 路由与请求绑定交给 group.Bind（从 g.Meta 读取）
		//
		// 为什么必须是一条链：见 responseChainGF 的注释——
		// 分开挂会让 bareJSON 写入时 statusWriter 已被恢复，日志失真。
		// 健康检查没有请求体：传 nil 表示不做 body 解码
		// （仍会净化 query/form 来源）。
		g.Middleware(responseChainGF(d.Log, d, d.Auth.SessionTTL(), nil))
		g.Bind(healthCtl)
	})

	// Agent 反向连接：认证由连接层负责（Bearer Token），与会话体系隔离。
	//
	// 注意：这是 WebSocket 升级路径，必须能拿到 http.Hijacker。
	// ghttp 的 Response.Writer 实现了 Hijacker，经 statusWriter 包装后
	// 仍透传（statusWriter 有 Hijack/Flush/Unwrap），因此整条链路可用。
	s.Group("/", func(group *ghttp.RouterGroup) {
		group.Middleware(gfChainWithSanitizer(d.Log, func(req *ghttp.Request) { writeBareResult(req, d) }, nil,
			func(_ *ghttp.Request, req *http.Request) (*http.Request, error) { return req, nil },
			recoverer(d.Log), requestLogger(d.Log), securityHeaders))
		group.Bind(&controller.AgentTransport{Handler: d.AgentConn})
	})

	// 登录不需要已有会话。
	//
	// **规范路由绑定**：路由由 api/auth/v1 的 g.Meta 声明，
	// group.Bind 由框架完成注册与请求绑定。
	// 审计、Cookie 安全属性、错误文案由 controller/auth.go 承载，
	// 业务仍调用共享的 auth service（不复制逻辑）。
	authCtl := controller.NewAuth(controller.AuthDeps{
		Auth:    d.Auth,
		Audit:   d.DB,
		Session: newSessionAccessor(),
		Log:     d.Log,
	})
	s.Group("/", func(g *ghttp.RouterGroup) {
		g.Middleware(responseChainGF(d.Log, d, d.Auth.SessionTTL(), func() any { return &authapi.LoginReq{} }))
		g.Bind(authCtl)
	})

	// ---------- 需要登录的路由 ----------
	//
	// 退出与 me 需要已在登录态，因此把 authenticator + auditMiddleware
	// **并入同一条链**（顺序在全局三层之后，与迁移前一致）。
	//
	// 为什么要单独的控制器实例：group.Bind 会注册该对象的**所有**方法，
	// 若把 login 也放进来会在同路径重复注册（GF 会 Fatalf 退出进程）。
	// 因此 Login 与 Logout/Me 用两个只暴露各自方法的对象。
	authPrivCtl := controller.NewAuthPrivate(controller.AuthDeps{
		Auth:    d.Auth,
		Audit:   d.DB,
		Session: newSessionAccessor(),
		Log:     d.Log,
	})
	s.Group("/", func(g *ghttp.RouterGroup) {
		// 一条链：全局三层 → authenticator → audit → 响应序列化。
		// 不拆成两个 g.Middleware：嵌套两条 gfChain 会让 writer 替换
		// 互相覆盖，statusWriter 失效。
		//
		// authenticator 用**原生 GF 守卫**形态：它可能提前拒绝，
		// 只有拿到 *ghttp.Request 才能用 commit 一次写完 GF 状态与响应。
		g.Middleware(privResponseChainGF(
			d.Log, d, d.Auth.SessionTTL(),
			func() any { return &authapi.MeReq{} },
			[]gate{authenticatorGF(d.Auth)},
			[]middlewareFunc{auditMiddleware(d.DB, d.Log)},
		))
		g.Bind(authPrivCtl)
	})

	bindOperationalRoutes(s, d)
	// 独立模块由各自 API 的 g.Meta 声明路由，入口只负责装配。
	bindManagementRoutes(s, d)
	bindIdentityRoutes(s, d)

	// ---------- 兜底 ----------
	//
	// GF 的兜底 handler 处理所有未命中的请求（含路径不存在与方法不匹配）。
	// 这里必须自己区分 /api 与前端路径，并保留"未命中也要过认证与审计"
	// 的语义——否则漏登记的写接口不会进审计，未登录探测会拿到 404 而非 401。
	s.BindHandler("/*", fallbackHandler(d))

	return s
}

// uniqueServerName 生成进程内唯一的 ghttp 实例名。
//
// 必须唯一的原因见 router_types.go 的 serverSeq 注释：
// 同名复用同一实例，重复注册同一路由会 Fatalf 终止进程。
func uniqueServerName() string {
	return "arkade-hub-" + itoa(nextServerSeq())
}

// itoa 是极小整数转字符串，避免为一点格式化引入额外依赖。
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
