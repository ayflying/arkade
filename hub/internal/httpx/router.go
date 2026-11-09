package httpx

// router.go 装配全部路由。
//
// 路由分层：
//   /agent/connect        Agent 反向连接（Bearer Token 认证，不经过会话中间件）
//   /api/auth/*           登录相关（login 不需要会话，logout/me 需要）
//   /api/*               业务接口（必须经过会话认证）
//   其余 GET             前端 SPA 回落（history 模式必需）

import (
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"

	"arkade/hub/internal/agentlink"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/store"
)

// Deps 是路由装配所需依赖，集中传入便于测试替换。
type Deps struct {
	Log        *slog.Logger
	DB         *store.DB
	Auth       *auth.Service
	Registry   *agentlink.Registry
	Ops        *agentlink.Ops
	AgentConn  http.Handler // Agent 连接处理器（不套会话中间件）
	WebDist    string       // 前端产物目录，空则不托管
	HubVersion string
}

// NewRouter 构造根路由。
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(recoverer(d.Log))
	r.Use(requestLogger(d.Log))
	r.Use(securityHeaders)

	// 健康检查：用于容器探活与部署校验，不需要认证。
	r.Get("/healthz", func(w http.ResponseWriter, req *http.Request) {
		ctx := req.Context()
		if err := d.DB.Ping(ctx); err != nil {
			respondError(w, d.Log, "internal", "数据库不可用")
			return
		}
		respondJSON(w, d.Log, http.StatusOK, map[string]any{
			"status":        "ok",
			"version":       d.HubVersion,
			"agents_online": d.Registry.Count(),
		})
	})

	// Agent 反向连接：认证由连接层负责（Bearer Token），与会话体系隔离。
	r.Get("/agent/connect", func(w http.ResponseWriter, req *http.Request) {
		d.AgentConn.ServeHTTP(w, req)
	})

	r.Route("/api", func(api chi.Router) {
		// 登录不需要已有会话。
		api.Post("/auth/login", handleLogin(d))

		// 之后所有接口都必须已登录。
		api.Group(func(priv chi.Router) {
			priv.Use(authenticator(d.Auth))

			priv.Post("/auth/logout", handleLogout(d))
			priv.Get("/auth/me", handleMe(d))

			priv.Get("/agents", handleListAgents(d))
			priv.Get("/agents/{agentID}/containers", handleListContainers(d))
			priv.Post("/agents/{agentID}/containers/{containerID}/start", handleContainerStart(d))
			priv.Post("/agents/{agentID}/containers/{containerID}/stop", handleContainerStop(d))
			priv.Get("/agents/{agentID}/containers/{containerID}/logs", handleContainerLogs(d))
		})
	})

	// 前端静态资源与 SPA 回落。
	if d.WebDist != "" {
		r.NotFound(spaHandler(d.WebDist, d.Log))
	} else {
		r.NotFound(func(w http.ResponseWriter, req *http.Request) {
			respondError(w, d.Log, "not_found", "接口不存在（Hub 未配置前端产物目录）")
		})
	}

	return r
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
