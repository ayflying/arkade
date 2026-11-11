package httpx

import (
	"github.com/gogf/gf/v2/net/ghttp"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

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
