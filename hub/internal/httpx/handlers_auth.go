package httpx

// handlers_auth.go 实现登录、退出与当前用户信息。

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"arkade/hub/internal/audit"
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
//
// 登录审计在这里手动记，是全项目唯一不走 auditMiddleware 的地方。
// 原因是它位于认证分组之外（用户还没有会话），而审计中间件
// 依赖 context 里的当前用户。手动记的代价是"新增登录入口时会漏"，
// 因此这里附一条守护测试（TestLoginAuditCoversBothOutcomes）。
//
// 安全约束（记审计时最容易踩的三个坑）：
//  1. **绝不记录密码**——请求体里有明文密码，连截断后的片段都不能留；
//  2. **绝不记录令牌**——响应里有 Cookie 与会话令牌，记下来等于泄露凭据；
//  3. **失败原因统一文案**——不能把 auth 层的原始错误写进审计，
//     否则 USER_NOT_FOUND / BAD_PASSWORD 这类区分会通过审计表
//     泄漏给任何能看审计的人（包括非 admin 看自己那条）。
//     账号是否存在是敏感信息，外部响应与内部审计都必须守住这一点。
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

		// 规范化用户名用于审计。
		//
		// 只记"用户输入的规范化名称"而不是回查数据库得到的名字：
		// 用户不存在时根本查不到，而"他尝试登录一个不存在的账号"
		// 这条信息恰恰是安全审计最需要的。
		// TrimSpace 与 auth 层保持一致，否则审计里会出现
		// " admin " 和 "admin" 两条看起来不同实为同一账号的记录。
		attempted := strings.TrimSpace(req.Username)

		res, err := d.Auth.Login(r.Context(), req.Username, req.Password)
		if err != nil {
			if errors.Is(err, auth.ErrInvalidCredentials) {
				// 统一提示，不区分「用户不存在」与「密码错误」，避免账号枚举。
				writeLoginAudit(d, attempted, "", auditLoginFailure)
				respondError(w, d.Log, envelope.CodeUnauthorized, "用户名或密码错误")
				return
			}
			// 内部错误（数据库故障等）。对外仍是模糊文案，
			// 审计里也只记"登录失败"，不记原始 err——
			// 原始错误可能含 SQL 片段或表名。
			d.Log.Error("登录过程出错", "err", err)
			writeLoginAudit(d, attempted, "", auditLoginFailure)
			respondError(w, d.Log, envelope.CodeInternal, "登录失败")
			return
		}

		// 成功：此时才有可信的 user_id 与用户名。
		writeLoginAudit(d, res.User.Username, res.User.ID, auditLoginSuccess)
		setSessionCookie(w, r, res.Token, res.ExpiresAt, d.Auth.SessionTTL())
		respondJSON(w, d.Log, http.StatusOK, userEnvelope{User: toUserView(res.User)})
	}
}

// 登录审计的结果取值。
const (
	auditLoginSuccess = audit.ResultSuccess
	auditLoginFailure = audit.ResultFailed
)

// writeLoginAudit 写入一条登录审计。
//
// 失败时只打日志、绝不改变登录响应：
// 用户登录成功却因为"审计写不进去"而看到错误，会以为密码错了，
// 接着可能反复尝试触发账号锁定——这是把旁路故障放大成可用性事故。
//
// 参数 username 在失败时是**用户输入的**名称（账号可能不存在），
// userID 在失败时为空：不能把"查不到的用户"编造一个 ID，
// 那会让审计表里的 user_id 失去可信度。
func writeLoginAudit(d Deps, username, userID, result string) {
	rec := audit.Record{
		ID:       audit.NewID(),
		UserID:   userID,
		Username: username,
		Action:   audit.ActionUserLogin,
		Result:   result,
		// 不填 Error：即使失败也不写具体原因，
		// 避免账号是否存在通过审计泄漏（见 handleLogin 的注释）。
		CreatedAt: time.Now().UTC(),
	}
	if err := d.DB.WriteAudit(rec); err != nil {
		d.Log.Error("写入登录审计失败（不影响登录响应）", "err", err)
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
