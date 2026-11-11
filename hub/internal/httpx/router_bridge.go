package httpx

import (
	"github.com/gogf/gf/v2/net/ghttp"
	"net/http"
)

// register 注册一条路由。
//
// 组装顺序（从外到内）：
//
//	recoverer → requestLogger → securityHeaders → [路由中间件…] → handler
//
// 整条链在 wrapToGF 里作用于同一个 r.Response.Writer，
// 所以 statusWriter 包装、Hijacker 透传都正常（见文件头说明）。
func register(
	s *ghttp.Server,
	method, pattern string,
	d Deps,
	mws []middlewareFunc,
	h httpHandlerFunc,
) {
	var chain http.Handler = h
	// 路由级中间件先包（它们在全局三层的内侧）。
	for i := len(mws) - 1; i >= 0; i-- {
		chain = mws[i](chain)
	}
	// 再包全局三层。
	chain = globalChain(chain, d.Log)

	s.BindHandler(method+":"+pattern, wrapToGF(pattern, chain))
}

// wrapToGF 把标准库 handler 包成 ghttp 的 handler。
//
// 它做两件必要的事：
//  1. 把 ghttp 的路径参数（GetRouter）与路由模板写入 request context，
//     供 pathParam / routePattern 读取；
//  2. 桥接到标准库请求/响应对象，让既有 handler 原样执行。
//
// **不包装 ResponseWriter**：直接把 r.Response.Writer 交给标准库链，
// 这样链上任何一个中间件包的 statusWriter 都能一路透传到最终写入，
// 不会被 GF 替换掉（这正是不能用 s.Use 的原因）。
func wrapToGF(pattern string, h http.Handler) func(*ghttp.Request) {
	params := paramsOf(pattern)
	return func(r *ghttp.Request) {
		ri := &routeInfo{Pattern: pattern}
		if len(params) > 0 {
			ri.Params = make(map[string]string, len(params))
			for _, name := range params {
				// 必须用 GetRouter 而不是 Get：
				// Get 会被同名 query/表单参数覆盖（见 routeparams.go 说明）。
				// GetRouter 返回 *gvar.Var，String() 取字符串值。
				ri.Params[name] = r.GetRouter(name).String()
			}
		}
		// 把 *ghttp.Request 一并放进 context：
		// ① 路径参数与路由模板（所有 handler 都要用）；
		// ② ghttp.Request 本身（仅 typed controller 需要，registerGF 取用）。
		//
		// 这样做而不是让控制器重新构造一个 Request：控制器拿到的必须是
		// **同一个**请求对象，否则响应缓冲、状态码、Cookie 会分叉到两个
		// 不同的 Response 上，出现"写了但没发出去"这类难查的问题。
		ctx := withRouteInfo(r.Request.Context(), ri)
		h.ServeHTTP(r.Response.Writer, r.Request.WithContext(ctx))
	}
}

// paramsOf 从模板中解析出参数名（按出现顺序）。
//
// 模板形如 /api/agents/{agentID}/containers/{containerID}。
func paramsOf(pattern string) []string {
	var names []string
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '{' {
			continue
		}
		j := i + 1
		for j < len(pattern) && pattern[j] != '}' {
			j++
		}
		if j < len(pattern) {
			names = append(names, pattern[i+1:j])
			i = j
		}
	}
	return names
}
