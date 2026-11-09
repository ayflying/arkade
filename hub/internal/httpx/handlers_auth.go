package httpx

// handlers_auth.go 实现登录、退出与当前用户信息。

import (
	"errors"
	"net/http"
	"time"

	"arkade/hub/internal/auth"
	"arkade/hub/internal/user"
	"arkade/protocol/envelope"
)

// loginRequest 是登录请求体。字段名与前端契约一致。
type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

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

// handleLogin 处理 POST /api/auth/login。
func handleLogin(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := decodeJSON(r, &req); err != nil {
			respondError(w, d.Log, envelope.CodeBadRequest, "请求格式错误")
			return
		}
		if req.Username == "" || req.Password == "" {
			respondError(w, d.Log, envelope.CodeBadRequest, "用户名和密码不能为空")
			return
		}

		res, err := d.Auth.Login(r.Context(), req.Username, req.Password)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				// 统一提示，不区分「用户不存在」与「密码错误」，避免账号枚举。
				respondError(w, d.Log, envelope.CodeUnauthorized, "用户名或密码错误")
				return
			}
			respondError(w, d.Log, envelope.CodeInternal, "登录失败")
			return
		}

		setSessionCookie(w, r, res.Token, res.ExpiresAt, d.Auth.SessionTTL())
		respondJSON(w, d.Log, http.StatusOK, userEnvelope{User: toUserView(res.User)})
	}
}

// handleLogout 处理 POST /api/auth/logout。
func handleLogout(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := d.Auth.Logout(r.Context(), tokenFrom(r.Context())); err != nil {
			d.Log.Warn("退出登录失败", "err", err)
		}
		// 无论服务端删除是否成功，都清除浏览器 Cookie，保证用户确实退出。
		clearSessionCookie(w, r)
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleMe 处理 GET /api/auth/me。
func handleMe(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := userFrom(r.Context())
		if !ok {
			// 理论上不可达（已过认证中间件），防御性处理。
			respondError(w, d.Log, envelope.CodeUnauthorized, "登录已失效")
			return
		}
		respondJSON(w, d.Log, http.StatusOK, userEnvelope{User: toUserView(u)})
	}
}

// setSessionCookie 写入会话 Cookie。
//
// 安全属性：
//   - HttpOnly：JS 读不到，降低 XSS 窃取风险；
//   - SameSite=Lax：阻止跨站 POST 携带 Cookie（CSRF 主防线）；
//   - Secure：仅在 HTTPS 下设置。Hub 常部署在内网 HTTP 后面，
//     如果强制 Secure，纯 HTTP 部署将无法登录，因此按请求协议判断。
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
