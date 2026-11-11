package httpx

// 本文件仅提供 SessionAccessor 的 Cookie 传输适配；
// 业务和对外视图由认证 controller 与 API 契约拥有。

import (
	"net/http"
	"time"

	"arkade/hub/internal/auth"
)

// setSessionCookie 写入会话 Cookie。
//
// 安全属性：
//   - HttpOnly：JS 读不到，降低 XSS 窃取风险；
//   - SameSite=Lax：阻止跨站 POST 携带 Cookie（CSRF 主防线）；
//   - Secure：仅在 HTTPS 下设置。Hub 常部署在内网 HTTP 后面，
//     强制 Secure 会让纯 HTTP 部署无法登录，因此按请求协议判断。
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    token,
		Path:     auth.CookiePath,
		Expires:  expires,
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie 清除会话 Cookie。
func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     auth.CookiePath,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// isHTTPS 判断请求是否经由 HTTPS。
// 支持反向代理转发头，否则在 Nginx 后面会误判为 HTTP 而漏设 Secure。
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto == "https"
	}
	return false
}
