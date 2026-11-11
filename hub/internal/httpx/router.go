package httpx

// router.go 装配全部路由。
//
// 路由分层：
//   /agent/connect        Agent 反向连接（Bearer Token 认证，不经过会话中间件）
//   /api/auth/*           登录相关（login 不需要会话，logout/me 需要）
//   /api/*               业务接口（必须经过会话认证）
//   其余 GET             前端 SPA 回落（history 模式必需）
//
// ## 迁移说明（GoFrame 阶段二）
//
// 路由全部由 **GoFrame 原生注册**（`ghttp.BindHandler("METHOD:/path", ...)`），
// 本包不再依赖 chi，也没有任何自建的路由匹配代码。
//
// 业务 handler 保持 `http.HandlerFunc` 签名不变，由 wrapToGF 桥接：
//
//	ghttp 负责：路径匹配、`{param}` 解析、404 兜底
//	wrapToGF 负责：取参写入 context（GetRouter）、桥接到标准库请求
//	handler 负责：业务逻辑（一行不改）
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
//	wrapToGF(pattern, recoverer(logger(security(handler))))
//
// 整条链始终作用于同一个 `r.Response.Writer`，writer 一路透传，
// 状态码、响应大小、Hijacker 全部正常。代价是每条路由各包一遍，
// 但这只是闭包构造，不涉及运行时开销。
//
// 兜底路由同样按这个方式包一遍（见 fallbackHandler）。

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
// 因此这里用进程内自增序号生成唯一名字（详见 tools/gf-http-probe 的实测记录）。
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
	register(s, http.MethodGet, "/agent/connect", d, nil, func(w http.ResponseWriter, req *http.Request) {
		d.AgentConn.ServeHTTP(w, req)
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
	// 桥接路由用的标准库链：authenticator → auditMiddleware。
	// 顺序与迁移前一致；audit 必须在认证之后，否则只能记到空用户名。
	privMW := []middlewareFunc{
		authenticator(d.Auth),
		auditMiddleware(d.DB, d.Log),
	}

	priv := func(method, pattern string, h httpHandlerFunc) {
		register(s, method, "/api"+pattern, d, privMW, h)
	}

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

	priv(http.MethodGet, "/agents", handleListAgents(d))
	priv(http.MethodGet, "/image-tasks", handleListImageTasks(d))
	priv(http.MethodGet, "/image-tasks/{taskID}", handleGetImageTask(d))
	priv(http.MethodPost, "/image-tasks/{taskID}/cancel", handleCancelImageTask(d))
	priv(http.MethodGet, "/image-tasks/{taskID}/download", handleDownloadImage(d))
	priv(http.MethodPost, "/agents/{agentID}/images/pull", handleStartImageTask(d, "pull"))
	priv(http.MethodPost, "/agents/{agentID}/images/export", handleStartImageTask(d, "export"))
	priv(http.MethodPost, "/agents/{agentID}/images/migrate", handleStartImageTask(d, "migrate"))
	priv(http.MethodPost, "/agents/{agentID}/images/import", handleUploadImage(d))
	// 操作审计：admin 看全部，非 admin 只看自己的（权限在 handler 内收紧）。
	priv(http.MethodGet, "/audit", handleListAudit(d))
	priv(http.MethodGet, "/audit/actions", handleListAuditActions(d))
	// 服务器记录的管理：改名/备注/禁用/删除。
	// 全部要求 admin（在 handler 里用 requireAdmin 判断），
	// 因为它们改的是纳管关系本身，会影响到所有用户组。
	priv(http.MethodPatch, "/agents/{agentID}", handlePatchAgent(d))
	// 预创建服务器：先建记录并下发专属密钥，再由那台机器接入。
	priv(http.MethodPost, "/agents", handleCreateAgent(d))
	// 重置专属密钥：旧密钥立即失效并踢掉现有连接。
	priv(http.MethodPost, "/agents/{agentID}/token/reset", handleResetAgentToken(d))
	priv(http.MethodDelete, "/agents/{agentID}", handleDeleteAgent(d))
	priv(http.MethodPost, "/agents/{agentID}/disable", handleDisableAgent(d))
	priv(http.MethodPost, "/agents/{agentID}/enable", handleEnableAgent(d))

	priv(http.MethodGet, "/agents/{agentID}/containers", handleListContainers(d))
	priv(http.MethodGet, "/agents/{agentID}/containers/{containerID}", handleContainerDetail(d))
	priv(http.MethodPost, "/agents/{agentID}/containers/{containerID}/start", handleContainerStart(d))
	priv(http.MethodPost, "/agents/{agentID}/containers/{containerID}/stop", handleContainerStop(d))
	// 重启与启停同级（operate）：不销毁对象、不改纳管关系。
	priv(http.MethodPost, "/agents/{agentID}/containers/{containerID}/restart", handleContainerRestart(d))
	// 删除是唯一不可逆的容器操作，权限提到 manage。
	priv(http.MethodDelete, "/agents/{agentID}/containers/{containerID}", handleContainerRemove(d))
	priv(http.MethodGet, "/agents/{agentID}/containers/{containerID}/logs", handleContainerLogs(d))

	// 容器编排（docker compose）。
	// 权限分档：列表/详情/日志/ps 要 view，动作要 operate，创建/更新/删除要 manage。
	// 分档在各自的 handler 里判断（requirePermission），不在这里按路径分组——
	// 同一前缀下不同动作的权限要求不同，路径分组表达不了这种差异。
	priv(http.MethodGet, "/agents/{agentID}/stacks", handleListStacks(d))
	priv(http.MethodPost, "/agents/{agentID}/stacks", handleCreateStack(d))
	priv(http.MethodPost, "/agents/{agentID}/stacks/import-preview", handleImportStackPreview(d))
	priv(http.MethodPost, "/agents/{agentID}/stacks/import", handleImportStack(d))
	priv(http.MethodGet, "/agents/{agentID}/external-stacks/{name}", handleExternalStack(d))
	priv(http.MethodGet, "/agents/{agentID}/external-stacks/{name}/config", handleExternalConfig(d))
	priv(http.MethodPut, "/agents/{agentID}/external-stacks/{name}/draft", handleExternalDraft(d))
	priv(http.MethodPost, "/agents/{agentID}/external-stacks/{name}/apply-preview", handleExternalPreview(d))
	priv(http.MethodPost, "/agents/{agentID}/external-stacks/{name}/apply", handleExternalApply(d))
	priv(http.MethodPost, "/agents/{agentID}/external-stacks/{name}/copy", handleExternalCopy(d))
	priv(http.MethodGet, "/agents/{agentID}/stacks/{name}", handleGetStack(d))
	priv(http.MethodPut, "/agents/{agentID}/stacks/{name}", handleUpdateStack(d))
	priv(http.MethodDelete, "/agents/{agentID}/stacks/{name}", handleDeleteStack(d))
	priv(http.MethodGet, "/agents/{agentID}/stacks/{name}/config", handleGetStackConfig(d))
	priv(http.MethodPost, "/agents/{agentID}/stacks/{name}/actions", handleStackAction(d))
	priv(http.MethodPost, "/agents/{agentID}/stacks/{name}/migrate", handleStartStackMigrate(d))
	priv(http.MethodPost, "/agents/{agentID}/stacks/{name}/operation-tasks", handleStartComposeOperation(d))
	priv(http.MethodGet, "/agents/{agentID}/stacks/{name}/operation-tasks/{taskID}", handleGetComposeOperation(d))
	priv(http.MethodPost, "/agents/{agentID}/stacks/{name}/pull-tasks", handleStartComposePull(d))
	priv(http.MethodGet, "/agents/{agentID}/stacks/{name}/pull-tasks/{taskID}", handleGetComposePull(d))
	priv(http.MethodGet, "/agents/{agentID}/stacks/{name}/logs", handleStackLogs(d))
	priv(http.MethodGet, "/agents/{agentID}/stacks/{name}/ps", handleStackPS(d))

	// 镜像 / 网络 / 存储卷（实时透传 Agent，Hub 不缓存）。
	priv(http.MethodGet, "/agents/{agentID}/images", handleListImages(d))
	priv(http.MethodDelete, "/agents/{agentID}/images/{imageID}", handleRemoveImage(d))
	priv(http.MethodGet, "/agents/{agentID}/networks", handleListNetworks(d))
	priv(http.MethodDelete, "/agents/{agentID}/networks/{networkID}", handleRemoveNetwork(d))
	priv(http.MethodPost, "/agents/{agentID}/volume-files", handleVolumeFiles(d))
	priv(http.MethodGet, "/agents/{agentID}/volumes", handleListVolumes(d))
	priv(http.MethodDelete, "/agents/{agentID}/volumes/{name}", handleRemoveVolume(d))

	// 用户管理（仅 admin，闸门在各自的 handler 里，见 requireAdmin）。
	priv(http.MethodGet, "/users", handleListUsers(d))
	priv(http.MethodPost, "/users", handleCreateUser(d))
	priv(http.MethodPatch, "/users/{userID}", handleUpdateUser(d))
	priv(http.MethodPut, "/users/{userID}/password", handleResetPassword(d))
	priv(http.MethodDelete, "/users/{userID}", handleDeleteUser(d))

	// 用户组管理（仅 admin）。
	priv(http.MethodGet, "/groups", handleListGroups(d))
	priv(http.MethodPost, "/groups", handleCreateGroup(d))
	priv(http.MethodPatch, "/groups/{groupID}", handleUpdateGroup(d))
	priv(http.MethodDelete, "/groups/{groupID}", handleDeleteGroup(d))
	priv(http.MethodGet, "/groups/{groupID}/members", handleListGroupMembers(d))
	priv(http.MethodPut, "/groups/{groupID}/members", handleSetGroupMembers(d))
	priv(http.MethodGet, "/groups/{groupID}/grants", handleListGroupGrants(d))
	priv(http.MethodPut, "/groups/{groupID}/grants", handleSetGroupGrants(d))

	// ---------- 兜底 ----------
	//
	// GF 的兜底 handler 处理所有未命中的请求（含路径不存在与方法不匹配）。
	// 这里必须自己区分 /api 与前端路径，并保留"未命中也要过认证与审计"
	// 的语义——否则漏登记的写接口不会进审计，未登录探测会拿到 404 而非 401。
	s.BindHandler("/*", fallbackHandler(d))

	return s
}

// register 注册一条路由。
//
// 组装顺序（从外到内）：
//
//	recoverer → requestLogger → securityHeaders → [路由中间件…] → handler
//
// 整条链在 wrapToGF 里作用于同一个 r.Response.Writer，
// 所以 statusWriter 包装、Hijacker 透传都正常（见文件头说明）。
func register(
	s *ghttp.Server,
	method, pattern string,
	d Deps,
	mws []middlewareFunc,
	h httpHandlerFunc,
) {
	var chain http.Handler = h
	// 路由级中间件先包（它们在全局三层的内侧）。
	for i := len(mws) - 1; i >= 0; i-- {
		chain = mws[i](chain)
	}
	// 再包全局三层。
	chain = globalChain(chain, d.Log)

	s.BindHandler(method+":"+pattern, wrapToGF(pattern, chain))
}

// wrapToGF 把标准库 handler 包成 ghttp 的 handler。
//
// 它做两件必要的事：
//  1. 把 ghttp 的路径参数（GetRouter）与路由模板写入 request context，
//     供 pathParam / routePattern 读取；
//  2. 桥接到标准库请求/响应对象，让既有 handler 原样执行。
//
// **不包装 ResponseWriter**：直接把 r.Response.Writer 交给标准库链，
// 这样链上任何一个中间件包的 statusWriter 都能一路透传到最终写入，
// 不会被 GF 替换掉（这正是不能用 s.Use 的原因）。
func wrapToGF(pattern string, h http.Handler) func(*ghttp.Request) {
	params := paramsOf(pattern)
	return func(r *ghttp.Request) {
		ri := &routeInfo{Pattern: pattern}
		if len(params) > 0 {
			ri.Params = make(map[string]string, len(params))
			for _, name := range params {
				// 必须用 GetRouter 而不是 Get：
				// Get 会被同名 query/表单参数覆盖（见 routeparams.go 说明）。
				// GetRouter 返回 *gvar.Var，String() 取字符串值。
				ri.Params[name] = r.GetRouter(name).String()
			}
		}
		// 把 *ghttp.Request 一并放进 context：
		// ① 路径参数与路由模板（所有 handler 都要用）；
		// ② ghttp.Request 本身（仅 typed controller 需要，registerGF 取用）。
		//
		// 这样做而不是让控制器重新构造一个 Request：控制器拿到的必须是
		// **同一个**请求对象，否则响应缓冲、状态码、Cookie 会分叉到两个
		// 不同的 Response 上，出现"写了但没发出去"这类难查的问题。
		ctx := withRouteInfo(r.Request.Context(), ri)
		h.ServeHTTP(r.Response.Writer, r.Request.WithContext(ctx))
	}
}

// paramsOf 从模板中解析出参数名（按出现顺序）。
//
// 模板形如 /api/agents/{agentID}/containers/{containerID}。
func paramsOf(pattern string) []string {
	var names []string
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '{' {
			continue
		}
		j := i + 1
		for j < len(pattern) && pattern[j] != '}' {
			j++
		}
		if j < len(pattern) {
			names = append(names, pattern[i+1:j])
			i = j
		}
	}
	return names
}

// fallbackHandler 处理 GF 未命中的请求。
//
// 覆盖两种情况：路径不存在、路径存在但方法不匹配（GF 都走到这里）。
//
// 语义与迁移前保持一致：
//   - /api 下未命中：先过认证与审计（漏登记的写接口要能进审计，
//     未登录探测要得到 401 而不是 404），再按 API 规则返回 JSON 404；
//   - 非 /api：走 SPA 回落。
//
// 两条分支同样按"全局三层 + 路由中间件"完整包一遍，
// 保证兜底响应的状态码/大小也被 requestLogger 记录（含 401/404/500）。
func fallbackHandler(d Deps) ghttp.HandlerFunc {
	privMW := []middlewareFunc{
		authenticator(d.Auth),
		auditMiddleware(d.DB, d.Log),
	}

	// API 未命中：认证 + 审计 + JSON 404。
	var apiChain http.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		respondError(w, d.Log, "not_found", "接口不存在")
	})
	for i := len(privMW) - 1; i >= 0; i-- {
		apiChain = privMW[i](apiChain)
	}
	apiChain = globalChain(apiChain, d.Log)

	// 非 API：SPA 回落（可能返回 index.html 或静态文件）。
	var spaChain http.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if d.WebDist != "" {
			spaHandler(d.WebDist, d.Log).ServeHTTP(w, req)
			return
		}
		respondError(w, d.Log, "not_found", "接口不存在（Hub 未配置前端产物目录）")
	})
	spaChain = globalChain(spaChain, d.Log)

	return func(r *ghttp.Request) {
		// 未命中任何模板，pattern 记空串：
		// 审计据此把 target 记成原始路径，便于发现"漏登记的接口"。
		req := r.Request.WithContext(withRouteInfo(r.Request.Context(), &routeInfo{Pattern: ""}))

		if isAPIPath(req.URL.Path) {
			apiChain.ServeHTTP(r.Response.Writer, req)
			return
		}
		spaChain.ServeHTTP(r.Response.Writer, req)
	}
}

// spaHandler 处理前端 history 模式的路由回落。
//
// 规则（web 侧明确要求）：
//   - /api/* 未命中一律 404 JSON，绝不回落到 index.html，
//     否则前端会拿到 HTML 去解析 JSON，报出无法理解的错误；
//   - 存在同名静态文件则返回文件；
//   - 其余 GET 返回 index.html，让前端路由接管（例如刷新 /agents/xxx）。
func spaHandler(dist string, log *slog.Logger) http.HandlerFunc {
	indexPath := filepath.Join(dist, "index.html")
	files := http.FileServer(http.Dir(dist))

	return func(w http.ResponseWriter, r *http.Request) {
		if isAPIPath(r.URL.Path) {
			respondError(w, log, "not_found", "接口不存在")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			respondError(w, log, "not_found", "资源不存在")
			return
		}

		clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		if clean != "." && clean != "" {
			candidate := filepath.Join(dist, clean)
			// 只允许访问产物目录内的文件，防目录穿越。
			if rel, err := filepath.Rel(dist, candidate); err == nil && !strings.HasPrefix(rel, "..") {
				if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
					files.ServeHTTP(w, r)
					return
				}
			}
		}

		// 回落：交给 index.html，由前端路由决定展示哪个页面。
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, indexPath)
	}
}

// isAPIPath 判断是否属于接口路径。
func isAPIPath(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/")
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
