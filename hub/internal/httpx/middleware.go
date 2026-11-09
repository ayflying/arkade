package httpx

// middleware.go 放通用 HTTP 中间件：请求日志、恢复、认证、权限、安全响应头。

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"arkade/hub/internal/auth"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// ctxKey 是 context 键的类型，避免与其它包的键冲突。
type ctxKey int

const (
	ctxKeyUser ctxKey = iota
	ctxKeyToken
)

// requestLogger 记录每个请求的耗时与状态码。
// 只记录 API 与连接请求，静态资源不记，避免日志刷屏。
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			log.Info("请求",
				"method", r.Method,
				"path", r.URL.Path,
				"status", sw.status,
				"cost", time.Since(start).String(),
				"remote", r.RemoteAddr,
			)
		})
	}
}

// statusWriter 捕获实际写出的状态码，供日志使用。
//
// 关键点：必须透传 Hijacker 接口。WebSocket 升级依赖 http.Hijacker 接管底层连接，
// 一旦被包装器遮断，Agent 建连会稳定失败并报
// "websocket: response does not implement http.Hijacker"，
// 表现为 Hub 返回 500 且 Agent 无限重连。
type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.written {
		s.status = code
		s.written = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	s.written = true
	return s.ResponseWriter.Write(b)
}

// Hijack 透传底层连接接管能力，供 WebSocket 升级使用。
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := s.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("底层 ResponseWriter 不支持 Hijack")
	}
	return hijacker.Hijack()
}

// Flush 透传刷新能力，避免流式响应被缓冲住。
func (s *statusWriter) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// recoverer 捕获 handler 中的 panic，避免单个请求打挂整个服务。
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					log.Error("请求处理发生 panic", "panic", rec, "path", r.URL.Path)
					respondError(w, log, envelope.CodeInternal, "服务器内部错误")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders 设置基础安全响应头。
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// authenticator 从 Cookie 解析当前用户，未登录时统一返回 401。
//
// 契约要求（web 侧确认）：所有接口未登录一律 401 + code=unauthorized，
// 绝不能用 200 + 空数据伪装成功，否则前端路由守卫会误判。
func authenticator(svc *auth.Service) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractToken(r)
			u, err := svc.Authenticate(r.Context(), token)
			if err != nil {
				respondError(w, slog.Default(), envelope.CodeUnauthorized, "登录已失效，请重新登录")
				return
			}
			ctx := context.WithValue(r.Context(), ctxKeyUser, u)
			ctx = context.WithValue(ctx, ctxKeyToken, token)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractToken 读取会话 Cookie。只认 Cookie，不认 URL 参数：
// 把令牌放进 URL 会被日志与浏览器历史留存。
func extractToken(r *http.Request) string {
	c, err := r.Cookie(auth.CookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// userFrom 从 context 取当前用户。调用方必须保证已过 authenticator。
func userFrom(ctx context.Context) (user.User, bool) {
	u, ok := ctx.Value(ctxKeyUser).(user.User)
	return u, ok
}

// tokenFrom 从 context 取当前会话令牌，供退出登录使用。
func tokenFrom(ctx context.Context) string {
	t, _ := ctx.Value(ctxKeyToken).(string)
	return t
}
