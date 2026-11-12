package controller

// auth.go 是认证接口的 GoFrame 规范控制器。
//
// ## 本文件承载的安全约定（迁移时必须逐条保住）
//
//  1. **绝不记录密码**——请求体里有明文密码，日志、审计都不能留片段；
//  2. **绝不记录令牌**——响应里有 Cookie 与会话令牌，记下来等于泄露凭据；
//  3. **失败原因统一文案**——不能把 auth 层的原始错误写进审计，
//     否则 USER_NOT_FOUND / BAD_PASSWORD 这类区分会通过审计表
//     泄漏给任何能看审计的人。账号是否存在是敏感信息，
//     对外响应与内部审计都必须守住。
//  4. Cookie 必须 HttpOnly + SameSite=Lax，Secure 按请求协议判断。
//
// ## 签名为何是 (ctx,*Req)(*Res,error)
//
// 对齐 GoFrame 规范路由：路由从 Req 的 g.Meta 读取，由 Bind 注册。
//
// 但登录要写 Set-Cookie、退出要返回 204 无正文——这两件事无法用
// 单纯的返回值表达。解决办法放在适配层（bindGF）：
//
//   - 需要写 Cookie 时，控制器通过 ctx 里携带的 response 句柄写入；
//   - 退出返回 `ErrNoContent` 哨兵，适配层据此写 204 且不写 body。
//
// 这样控制器本身仍然"只返回数据/错误"，不直接操作网络写出口。

import (
	"context"
	"errors"
	"github.com/gogf/gf/v2/net/ghttp"
	"net"
	"net/http"
	"strings"
	"time"

	authapi "arkade/hub/api/auth/v1"
	"arkade/hub/internal/audit"
	"arkade/hub/internal/auth"
	"arkade/hub/internal/user"
)

// AuthDeps 是认证控制器的依赖（按能力分组，不按函数罗列）。
type AuthDeps struct {
	// Auth 是认证业务服务（共享实现，不在此复制业务逻辑）。
	Auth AuthService
	// Audit 写审计记录。
	Audit AuditWriter
	// Session 提供当前请求的会话读取能力。
	Session SessionAccessor

	// Log 记日志。
	Log Logger

	// SessionTTL 是会话有效期（用于 Cookie MaxAge）。
	SessionTTL time.Duration
}

// AuthService 是认证业务能力。
type AuthService interface {
	Login(ctx context.Context, username, password string) (auth.LoginResult, error)
	Logout(ctx context.Context, token string) error
}

// AuditWriter 是审计落库能力。
type AuditWriter interface {
	WriteAudit(rec audit.Record) error
}

// SessionAccessor 提供从当前请求读取会话状态的能力。
type SessionAccessor interface {
	// CurrentUser 取当前登录用户。ok=false 表示未登录。
	CurrentUser(ctx context.Context) (user.User, bool)
	// Token 取会话令牌（退出时用于服务端删除）。
	Token(ctx context.Context) string
	// SetCookie 写会话 Cookie。
	SetCookie(ctx context.Context, token string, expires time.Time)
	// ClearCookie 清会话 Cookie。
	ClearCookie(ctx context.Context)
}

// Logger 是控制器用的日志接口。
type Logger interface {
	Error(msg string, args ...any)
	Warn(msg string, args ...any)
}

// Auth 是登录控制器。
//
// 只暴露 Login：`group.Bind` 会注册对象上的**所有**方法，
// 把 Logout/Me 放在这里会导致它们被注册到公开组
// （绕过 authenticator），且与私有组重复注册时 GF 会 Fatalf。
// 因此按"是否需要已登录"拆成两个对象，见 AuthPrivate。
type Auth struct {
	deps AuthDeps
}

// NewAuth 构造登录控制器。
func NewAuth(d AuthDeps) *Auth { return &Auth{deps: d} }

// AuthPrivate 是需要登录态的控制器（Logout / Me）。
type AuthPrivate struct {
	deps AuthDeps
}

// NewAuthPrivate 构造需登录态的控制器。
func NewAuthPrivate(d AuthDeps) *AuthPrivate { return &AuthPrivate{deps: d} }

// 登录审计的结果取值（与 audit 包保持一致）。
const (
	auditLoginSuccess = audit.ResultSuccess
	auditLoginFailure = audit.ResultFailed
)

// ErrNoContent 是"成功但无正文"的哨兵（退出登录返回 204）。
//
// 定义在控制器层而不是直接写 204：控制器不接触网络写出口，
// 由适配层把该哨兵翻译成 204 + 空 body。
var ErrNoContent = errors.New("no content")

// Login 处理 POST /api/auth/login。
//
// 成功：200 + Set-Cookie + {"user":{…}}
// 失败：400（格式/空值）或 401（凭据错误）或 500（内部错误），
// 且**无论哪种失败都会记一条审计**。
func (c *Auth) Login(ctx context.Context, req *authapi.LoginReq) (*authapi.LoginRes, error) {
	if req.Username == "" || req.Password == "" {
		return nil, &Error{Status: http.StatusBadRequest, Code: "bad_request", Message: "用户名和密码不能为空"}
	}

	// 规范化用户名用于审计。
	//
	// 只记"用户输入的规范化名称"而不是回查数据库得到的名字：
	// 用户不存在时根本查不到，而"他尝试登录一个不存在的账号"
	// 这条信息恰恰是安全审计最需要的。
	// TrimSpace 与 auth 层保持一致，否则审计里会出现
	// " admin " 和 "admin" 两条看起来不同实为同一账号的记录。
	attempted := strings.TrimSpace(req.Username)

	var res auth.LoginResult
	var err error
	if limiter, ok := c.deps.Auth.(interface {
		LoginWithClient(context.Context, string, string, string) (auth.LoginResult, error)
	}); ok {
		ip := ""
		if r := ghttp.RequestFromCtx(ctx); r != nil {
			ip, _, _ = net.SplitHostPort(r.RemoteAddr)
		}
		res, err = limiter.LoginWithClient(ctx, req.Username, req.Password, ip)
	} else {
		res, err = c.deps.Auth.Login(ctx, req.Username, req.Password)
	}
	if err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			c.writeLoginAudit(attempted, "", auditLoginFailure)
			return nil, &Error{Status: http.StatusTooManyRequests, Code: "rate_limited", Message: "登录尝试过多，请稍后重试"}
		}
		if errors.Is(err, auth.ErrInvalidCredentials) {
			// 统一提示，不区分「用户不存在」与「密码错误」，避免账号枚举。
			c.writeLoginAudit(attempted, "", auditLoginFailure)
			return nil, &Error{Status: http.StatusUnauthorized, Code: "unauthorized", Message: "用户名或密码错误"}
		}
		// 内部错误（数据库故障等）。对外仍是模糊文案，
		// 审计里也只记"登录失败"，不记原始 err——
		// 原始错误可能含 SQL 片段或表名。
		c.deps.Log.Error("登录过程出错", "err", err)
		c.writeLoginAudit(attempted, "", auditLoginFailure)
		return nil, ErrInternal.WithErr(err)
	}

	// 成功：此时才有可信的 user_id 与用户名。
	c.writeLoginAudit(res.User.Username, res.User.ID, auditLoginSuccess)
	c.deps.Session.SetCookie(ctx, res.Token, res.ExpiresAt)
	return &authapi.LoginRes{User: UserViewOf(res.User)}, nil
}

// writeLoginAudit 写入一条登录审计。
//
// 失败时只打日志、绝不改变登录响应：
// 用户登录成功却因为"审计写不进去"而看到错误，会以为密码错了，
// 接着可能反复尝试触发账号锁定——这是把旁路故障放大成可用性事故。
//
// username 在失败时是**用户输入的**名称（账号可能不存在），
// userID 在失败时为空：不能把"查不到的用户"编造一个 ID，
// 那会让审计表里的 user_id 失去可信度。
func (c *Auth) writeLoginAudit(username, userID, result string) {
	rec := audit.Record{
		ID:       audit.NewID(),
		UserID:   userID,
		Username: username,
		Action:   audit.ActionUserLogin,
		Result:   result,
		// 不填 Error：即使失败也不写具体原因，
		// 避免账号是否存在通过审计泄漏（见 Login 的注释）。
		CreatedAt: time.Now().UTC(),
	}
	if err := c.deps.Audit.WriteAudit(rec); err != nil {
		c.deps.Log.Error("写入登录审计失败（不影响登录响应）", "err", err)
	}
}

// Logout 处理 POST /api/auth/logout。
//
// 成功返回 ErrNoContent（适配层写 204 + 空 body）。
// 无论服务端删除是否成功都清除 Cookie：
// 服务端失败时至少浏览器不再持有有效凭据，用户确实退出。
func (c *AuthPrivate) Logout(ctx context.Context, _ *authapi.LogoutReq) (*authapi.LogoutRes, error) {
	if err := c.deps.Auth.Logout(ctx, c.deps.Session.Token(ctx)); err != nil {
		c.deps.Log.Warn("退出登录失败", "err", err)
	}
	c.deps.Session.ClearCookie(ctx)
	return nil, ErrNoContent
}

// Me 处理 GET /api/auth/me。
func (c *AuthPrivate) Me(ctx context.Context, _ *authapi.MeReq) (*authapi.MeRes, error) {
	u, ok := c.deps.Session.CurrentUser(ctx)
	if !ok {
		// 理论上不可达（已过认证中间件），防御性处理。
		return nil, ErrUnauthorized
	}
	return &authapi.MeRes{User: UserViewOf(u)}, nil
}

// UserViewOf 把内部用户模型映射为对外视图。
//
// 映射而非直接序列化是**安全要求**：user.User 带 PasswordHash，
// 用它做响应类型意味着"口令哈希是否被序列化"取决于 tag，
// 一旦有人补了 json tag 就会泄漏。这里只挑允许上网的字段。
func UserViewOf(u user.User) authapi.UserView {
	return authapi.UserView{
		ID:                 u.ID,
		Username:           u.Username,
		DisplayName:        u.DisplayName,
		Role:               string(u.Role),
		MustChangePassword: u.MustChangePassword,
		LocalPassword:      u.PasswordHash != "",
	}
}
