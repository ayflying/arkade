package httpx

// handlers_auth_view.go 提供登录与用户接口共用的对外视图类型。
//
// 登录、退出、当前用户三个接口已迁到 GoFrame 规范控制器
// （见 internal/controller/auth.go + api/auth/v1），
// 这里只留下仍被其它 handler 与 sessionAccessor 使用的共享定义。

import (
	"net/http"
	"time"

	"arkade/hub/internal/auth"
	"arkade/hub/internal/user"
)

// userView 是返回给前端的用户信息。
// 单独定义而不是直接序列化 user.User，是为了确保永远不会把 PasswordHash 带上。
type userView struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

// userEnvelope 是登录与 /auth/me 的统一响应体：{"user":{...}}。
type userEnvelope struct {
	User userView `json:"user"`
}

// toUserView 把内部模型转成对外视图。
func toUserView(u user.User) userView {
	return userView{
		ID:          u.ID,
		Username:    u.Username,
		DisplayName: u.DisplayName,
		Role:        string(u.Role),
	}
}

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
