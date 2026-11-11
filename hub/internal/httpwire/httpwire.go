// Package httpwire 提供 HTTP 报文层面的公共写出口。
//
// ## 为什么单独成一个包
//
// 这些函数（JSON、错误信封、会话 Cookie、body 解码）被两类代码共用：
//
//	httpx      既有标准库 handler
//	controller typed GoFrame 控制器
//
// 若让 controller 依赖 httpx，会和 httpx → controller 的路由装配
// 形成 import 循环。把它们下沉到不依赖任何业务包的最低层，
// 两边都只依赖 httpwire，**实现只有一份**，契约不会分叉。
//
// ## 契约要点（改动即破坏前端）
//
//   - 响应体是**顶层裸 JSON**，不能有 {code,message,data} 包装；
//   - 错误形状固定 {"error":{"code":"…","message":"…"}}；
//   - 状态码由调用方决定（code → status 的推断保留在 httpx 兼容层）；
//   - 会话 Cookie：HttpOnly、SameSite=Lax、Secure 按请求协议判断；
//   - 所有 JSON 响应带 Cache-Control: no-store，避免带凭据的响应被缓存。
package httpwire

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// MaxBodyBytes 是请求体上限。
//
// 4 MiB 足够编排文件，远超普通接口需要；限制它的目的是避免
// 恶意或误发的大 body 把内存撑爆。
const MaxBodyBytes = 4 << 20

// ============================ 时间与 Cookie 常量 ============================
//
// Cookie 属性由 auth 包定义（CookieName/CookiePath），这里只关心安全属性。

// SessionCookie 描述一个会话 Cookie 的写入内容。
type SessionCookie struct {
	Name     string
	Value    string
	Path     string
	Expires  time.Time
	TTL      time.Duration
	HTTPOnly bool
	SameSite http.SameSite
}

// WriteSessionCookie 写入会话 Cookie。
//
// Secure 属性按请求协议判断：Hub 常部署在内网 HTTP 后面，
// 强制 Secure 会让纯 HTTP 部署无法登录。
func WriteSessionCookie(w http.ResponseWriter, r *http.Request, c SessionCookie) {
	http.SetCookie(w, &http.Cookie{
		Name:     c.Name,
		Value:    c.Value,
		Path:     c.Path,
		Expires:  c.Expires,
		MaxAge:   int(c.TTL.Seconds()),
		HttpOnly: c.HTTPOnly,
		Secure:   IsHTTPS(r),
		SameSite: c.SameSite,
	})
}

// ClearCookie 清除一个 Cookie（MaxAge=-1）。
func ClearCookie(w http.ResponseWriter, r *http.Request, name, path string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     path,
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   IsHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	})
}

// IsHTTPS 判断请求是否经由 HTTPS。
//
// 支持反向代理转发头，否则在 Nginx 后面会误判为 HTTP 而漏设 Secure。
func IsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto == "https"
	}
	return false
}

// ============================ JSON 写出口 ============================

// WriteJSON 写出 JSON 响应。
//
// 行为约定（与迁移前逐字节一致）：
//   - Content-Type: application/json; charset=utf-8
//   - Cache-Control: no-store（带凭据的响应不能被缓存）
//   - 先 WriteHeader 再写 body，避免 net/http 隐式 200 覆盖业务状态码
func WriteJSON(w http.ResponseWriter, log *slog.Logger, status int, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		// 序列化失败属于代码缺陷（返回了不可序列化的类型），
		// 但不能因此给客户端一个空 200——改回 500 并记日志。
		log.Error("响应序列化失败", "err", err)
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"code":"internal","message":"服务器内部错误，请联系管理员查看日志"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// ErrorBody 是错误响应里 error 对象的字段。
type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ErrorEnvelope 是错误响应体：{"error":{"code":"…","message":"…"}}。
//
// 单层包装是前端契约的一部分：前端按 body.error.code 分支处理，
// 多加一层或改名都会让所有错误提示失效。
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// WriteError 按固定形状写出错误响应。
func WriteError(w http.ResponseWriter, log *slog.Logger, status int, code, message string) {
	WriteJSON(w, log, status, ErrorEnvelope{Error: ErrorBody{Code: code, Message: message}})
}

// ============================ 请求解码 ============================

// DecodeJSON 把请求体解析到 out，拒绝未知字段。
//
// DisallowUnknownFields 是刻意的：前端多传字段说明契约已漂移，
// 静默忽略会让两边长期不一致却不被发现。这条规则与迁移前一致，
// 不是本次新增的严格化——放宽它会掩盖前端 bug，收紧它会影响现有调用。
//
// 空 body 视为错误（调用方按 400 处理）：这些接口都要求 JSON 请求体，
// 静默接受空 body 会让"忘了发数据"变成"字段全空的合法请求"。
// LimitReader 兜住超大 body，避免内存被撑爆。
func DecodeJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return errors.New("请求体为空")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
