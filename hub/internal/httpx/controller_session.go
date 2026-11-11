package httpx

// controller_session.go 把会话能力接到控制器接口上。
//
// ## 关键：每请求状态绝不放在 controller 上
//
// `controller.Auth` 是**共享单例**（NewRouter 时创建一次，所有请求共用）。
// 若把当前请求的 *ghttp.Request 存成它的字段，并发请求会互相覆盖，
// 表现为"用户 A 拿到用户 B 的 Cookie"——这是最严重的越权类缺陷。
//
// 所以所有请求态都从**参数**进入：
//
//	CurrentUser(ctx) / Token(ctx)  ← ctx（authenticator 中间件写入）
//	SetCookie / ClearCookie        ← ctx → ghttp.RequestFromCtx → Response
//
// ## 用 GF 官方的 RequestFromCtx
//
// `(*ghttp.Request).Context()` 在返回前会自动把 Request 注入 context
// （ghttp_request_param_ctx.go:24：`ctxKeyForRequest`），
// 因此 `ghttp.RequestFromCtx(ctx)` 能安全取回当前请求对象，
// 不需要自建 context 键或 Holder。
//
// 这也意味着控制器的 `ctx` 必须是**当前请求自己的** context——
// gfChain 链底已把中间件处理过的 req 写回 r.Request，满足这一点。

import (
	"context"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"arkade/hub/internal/auth"
	"arkade/hub/internal/user"
)

// sessionAccessor 实现 controller.SessionAccessor。
//
// 它是**无状态**的：不持有任何请求相关字段，可安全地作为共享实例使用。
type sessionAccessor struct{}

// newSessionAccessor 构造会话访问器。
func newSessionAccessor() sessionAccessor { return sessionAccessor{} }

// CurrentUser 取当前登录用户（由 authenticator 中间件写入 context）。
func (sessionAccessor) CurrentUser(ctx context.Context) (user.User, bool) {
	return userFrom(ctx)
}

// Token 取会话令牌。
func (sessionAccessor) Token(ctx context.Context) string {
	return tokenFrom(ctx)
}

// SetCookie 写会话 Cookie。
//
// 从 ctx 反查当前请求；取不到时静默跳过——那意味着调用方没在请求
// 生命周期内使用，此时写不写 Cookie 都不影响正确性。
func (sessionAccessor) SetCookie(ctx context.Context, token string, expires time.Time) {
	r := ghttp.RequestFromCtx(ctx)
	if r == nil {
		return
	}
	setSessionCookie(r.Response.Writer, r.Request, token, expires, sessionTTLFromCtx(ctx))
}

// ClearCookie 清会话 Cookie。
func (sessionAccessor) ClearCookie(ctx context.Context) {
	r := ghttp.RequestFromCtx(ctx)
	if r == nil {
		return
	}
	clearSessionCookie(r.Response.Writer, r.Request)
}

// sessionTTLKey 是会话有效期在 context 里的键。
//
// 有效期是**配置值**（不是请求态），由 registerAuthRoutes 在装配时
// 放进 context，因此不存在并发覆盖问题。
type sessionTTLKey struct{}

// withSessionTTL 把会话有效期放进 context。
func withSessionTTL(ctx context.Context, ttl time.Duration) context.Context {
	return context.WithValue(ctx, sessionTTLKey{}, ttl)
}

// sessionTTLFromCtx 取会话有效期。
func sessionTTLFromCtx(ctx context.Context) time.Duration {
	if v, ok := ctx.Value(sessionTTLKey{}).(time.Duration); ok {
		return v
	}
	return 0
}

// 保证 auth 包被引用：CookieName/CookiePath 由它定义，
// setSessionCookie/clearSessionCookie 内部使用。
var _ = auth.CookieName
