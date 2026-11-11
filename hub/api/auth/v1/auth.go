// Package v1 定义认证接口的 GoFrame 标准契约。
//
// ## 契约声明方式
//
// 每个请求结构体内嵌 `g.Meta` 声明路径与方法，路由由框架读取类型得到。
//
// ## 兼容性约束（不可违反）
//
//  1. 响应体是**顶层裸 JSON**（`{"user":{…}}`），不能包装成
//     `{"code":0,"message":"","data":…}` —— 即不得启用
//     MiddlewareHandlerResponse。控制器直接写响应。
//  2. 状态码语义保持不变：登录 200 + Set-Cookie，退出 204，me 200。
//  3. 错误响应形状固定为 `{"error":{"code":"…","message":"…"}}`，
//     由控制器统一产出，本包不定义错误类型——
//     全站错误格式只能有一处来源。
//  4. Cookie 安全属性（HttpOnly / SameSite=Lax / 条件 Secure）
//     由 httpwire 统一设置，本包不参与决策。
package v1

import "github.com/gogf/gf/v2/frame/g"

// ============================ POST /api/auth/login ============================

// LoginReq 是登录请求体。
//
// 字段名与前端一致：前端直接发 `{"username":"…","password":"…"}`。
// 两个字段都必填，空值由控制器判为 400。
type LoginReq struct {
	g.Meta   `path:"/api/auth/login" method:"post" tags:"Auth" summary:"登录"`
	Username string `json:"username" dc:"用户名"`
	Password string `json:"password" dc:"密码"`
}

// LoginRes 是登录成功响应：{"user":{…}}。
type LoginRes struct {
	User UserView `json:"user" dc:"当前登录用户"`
}

// ============================ POST /api/auth/logout ============================

// LogoutReq 是退出登录请求。
//
// 无参数字段：身份来自会话 Cookie。
type LogoutReq struct {
	g.Meta `path:"/api/auth/logout" method:"post" tags:"Auth" summary:"退出登录"`
}

// LogoutRes 是退出响应。实际返回 204 无正文，此结构仅用于声明契约。
type LogoutRes struct{}

// ============================ GET /api/auth/me ============================

// MeReq 是当前用户请求（无参数）。
type MeReq struct {
	g.Meta `path:"/api/auth/me" method:"get" tags:"Auth" summary:"当前用户"`
}

// MeRes 是当前用户响应：{"user":{…}}。
type MeRes struct {
	User UserView `json:"user" dc:"当前登录用户"`
}

// ============================ 公共结构 ============================

// UserView 是返回给前端的用户信息。
//
// 单独定义而不是直接序列化内部 user.User：
// user.User 带 PasswordHash 字段，用它做响应类型等于把口令哈希的
// 存在性暴露给序列化逻辑——一旦将来有人加了 json tag，泄漏就发生了。
// 这里只列**允许出现在网络上**的字段。
type UserView struct {
	ID          string `json:"id" dc:"用户 ID"`
	Username    string `json:"username" dc:"登录名"`
	DisplayName string `json:"display_name" dc:"显示名"`
	Role        string `json:"role" dc:"角色"`
}
