package httpx

// routeparams.go 是路径参数与路由模板的统一读取入口。
//
// ## 迁移背景
//
// 迁移前 httpx 用 chi 做路由，取参一律 `chi.URLParam(r, "agentID")`，共 40+ 处。
// chi 把匹配结果塞进 context 的 *chi.Context，URLParam 再从那里取。
//
// 换到 GoFrame 后，若调用点不改就会**静默返回空串**（不 panic、不报错），
// 后果是查不到记录 → 404 "服务器不存在"，看起来像数据问题，定位成本极高。
// audit_routes.go:122 的注释专门警告过这个坑。
//
// ## 本次方案（原生 GF 路由，不做任何自建匹配）
//
// GoFrame 负责路由匹配，但 handler 仍保持 `http.HandlerFunc` 签名——
// 由一个极薄的 wrapper 把 (ghttp.Request → 标准库请求) 桥接起来，
// 同时把两样东西放进 context：
//
//  1. **路径参数**：从 `r.GetRouter(key)` 取，写入 context 供 pathParam 读取；
//  2. **路由模板**：注册时就是确定的字符串，直接写入，供审计查动作映射表。
//
// 这样 40+ 处 handler 只需把 `chi.URLParam(r, "x")` 换成 `pathParam(r, "x")`，
// 签名与业务逻辑一行不动。
//
// ## 为什么不用 ghttp 的 r.Get()
//
// `r.Get` 是"路由参数 + query + 表单"的统一入口，**同名时后者覆盖前者**。
// 取路径参数必须用 `r.GetRouter`，否则 `?agentID=别人的ID` 会让 handler
// 拿到别人的 ID。这是取值来源错误，必须避免。
// （注：这只说明取错值，不构成越权证明——是否越权取决于 handler 后续怎么用。）

import (
	"context"
	"net/http"
)

// routeCtxKey 是路由信息在 context 中的键。
type routeCtxKey struct{}

// routeInfo 是一次请求的路由信息，由 wrapper 在进入 handler 前写入。
type routeInfo struct {
	// Pattern 是注册时用的路由模板（含 {agentID} 占位符），
	// 审计中间件按它查动作映射表（audit_routes.go 的键都是模板形式）。
	Pattern string
	// Params 是已解析好的路径参数（来自 ghttp 的 GetRouter）。
	Params map[string]string
}

// withRouteInfo 把路由信息放进 context。
func withRouteInfo(ctx context.Context, ri *routeInfo) context.Context {
	return context.WithValue(ctx, routeCtxKey{}, ri)
}

// routeInfoFrom 从 context 取路由信息，没有则返回 nil。
func routeInfoFrom(ctx context.Context) *routeInfo {
	ri, _ := ctx.Value(routeCtxKey{}).(*routeInfo)
	return ri
}

// pathParam 是**全包唯一的路径参数读取入口**。
//
// 所有 handler 都必须用它取路径参数，不得直接用 chi.URLParam
// 或 ghttp 的 r.Get/r.GetRouter。理由见文件头。
func pathParam(r *http.Request, key string) string {
	if ri := routeInfoFrom(r.Context()); ri != nil {
		return ri.Params[key]
	}
	return ""
}

// routePattern 取当前请求匹配到的路由模板。
//
// 审计中间件用它查动作映射表。由于 wrapper 在进入中间件链之前
// 就已写入，本函数在中间件里即可用（不存在 chi 那种"必须等 handler
// 执行完才能从 RouteContext 取"的时序依赖）。
func routePattern(r *http.Request) string {
	if ri := routeInfoFrom(r.Context()); ri != nil {
		return ri.Pattern
	}
	return ""
}
