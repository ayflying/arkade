package httpx

// gf_middleware.go 把标准库形态的中间件接进 GoFrame 的请求链。
//
// 既有中间件都是 `func(next http.Handler) http.Handler`，靠包装
// http.ResponseWriter 工作（requestLogger 的 statusWriter 依赖它记状态码）。
// 接法：把 `r.Middleware.Next()` 当作标准库链的最内层——
// 包到最后调 Next()，GF 走到控制器，返回时逐层回来。
//
// 两个必须守住的不变量：
//
//  1. 入口用 start.ResponseWriter（原始底层 writer），不能用 start 本身。
//     否则 statusWriter 指向 start、start 又指向 statusWriter，写成递归。
//  2. 写响应必须在 Next() 返回后、writer 恢复的 defer 之前，
//     否则 statusWriter 已摘掉，日志与 GF 计数都会失真。

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gogf/gf/v2/net/ghttp"

	"arkade/hub/internal/auth"
	"arkade/protocol/envelope"
)

// gfChain 构造 GF 中间件，内部按顺序套用标准库中间件。
//
// 传入顺序即从外到内（recoverer 必须在最外层才能兜住后面所有层）。
//
// afterNext 是可选的链底后置动作（可为 nil），在 `Next()` 返回后、
// **writer 替换仍然生效期间**调用——这是能同时记录
// "标准库链状态日志" 与 "GF 内部 BytesWritten" 的唯一位置。
// 它不能做成独立中间件：那样会被包在 mws 外层，且链底 defer
// 恢复 writer 之后才执行，两者都会失真。
//
// prepare 是可选的 context 增强（可为 nil）：进入标准库链前对
// context 做一次性加工（例如放入会话有效期这类配置值）。
// 配置值不是请求态，放 context 不会造成并发覆盖。
//
// guardTarget 是可选的请求体校验靶子（可为 nil，表示不校验）。
//
// 必须是**每请求新建实例的工厂**，不能传共享的 *Req 指针：
// 该控制器/中间件是单例，多请求并发时共享指针会让
// json.Decode 写同一块内存（数据竞争），并把上一个请求的字段残值
// 带到下一个请求里。
func gfChain(
	log *slog.Logger,
	afterNext func(*ghttp.Request),
	prepare func(context.Context) context.Context,
	guardTarget func() any,
	mws ...middlewareFunc,
) ghttp.HandlerFunc {
	return func(r *ghttp.Request) {
		start := r.Response.Writer

		// 链底：同步 writer 与请求，跑到控制器，然后**在恢复前**写出响应。
		var chain http.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			// 把最外层包装后的 writer 装到 GF 的 *response.Writer 上
			// （它内嵌 ResponseWriter 字段，可替换）。
			// 之后任何对 GF writer 的写入都会经过标准库链最外层包装，
			// 于是 statusWriter 能记到状态与大小，GF 的 bytesWritten 也累加。
			old := start.ResponseWriter
			start.ResponseWriter = w
			defer func() { start.ResponseWriter = old }()

			// 严格校验请求体并净化参数来源：必须在 GF 的
			// `r.Middleware.Next()`（内部会 Parse）之前。
			//
			// GF 默认合并 query/form/body 多来源参数，这是它的设计；
			// 本项目的契约是"只认 JSON body"，所以这里按契约收敛来源。
			//
			// 靶子在**每个请求内新建**：共享实例会被并发写，
			// 也会把残值带到下一个请求。
			var target any
			if guardTarget != nil {
				target = guardTarget()
			}
			guarded, gerr := sanitizeBody(req, target)
			req = guarded
			r.Request = req
			if gerr != nil {
				// 校验失败：不进入控制器，直接 400。
				commit(r, log, http.StatusBadRequest, nil, "bad_request", gerr.Error())
				return
			}

			// 后续 GF 中间件与控制器拿到的必须是**链上这份**请求：
			// authenticator 等中间件把身份写进了它的 context。
			// 同步执行到控制器；返回时 Res/error 已在 ghttp.Request 上。
			r.Middleware.Next()

			// 关键：必须在 defer 恢复 writer **之前**输出响应，
			// 否则 statusWriter 已摘掉，日志与计数都会失真。
			if afterNext != nil {
				afterNext(r)
			}
		})

		// 从内向外套，因此倒序遍历。
		for i := len(mws) - 1; i >= 0; i-- {
			chain = mws[i](chain)
		}

		// 入口用**原始底层 writer**，不能用 start（会形成自引用）。
		req := r.Request
		if prepare != nil {
			req = req.WithContext(prepare(req.Context()))
		}
		chain.ServeHTTP(start.ResponseWriter, req)
	}
}

// authenticatorGF 认证当前请求，未登录时直接用 commit 写出 401。
//
// ## 为什么不能沿用标准库形态
//
// 标准库版本在链中间提前写出后直接 return，此时：
//
//   - 链底的 afterNext（响应序列化所在）根本不会执行；
//   - 写入绕过了 GF 的 *response.Writer，于是
//     BytesWritten()==0 且 Status==0；
//   - GF 的 handleResponse 认为"没写过响应"，又补一次状态文本
//     （实测：错误体 code 为空，日志 superfluous WriteHeader）。
//
// 拿到 *ghttp.Request 才能用 commit 一次写完 GF 状态与响应。
//
// ## 返回值约定
//
// 它**不调用** r.Middleware.Next()：调用方（privResponseChainGF 的
// 包装器）负责在认证通过后推进标准库链。GF 的 Next() 与标准库链的
// next 是两条不同的栈，混用会把链走两遍。
//
// 返回 false 表示已提前拒绝、不应继续。
func authenticatorGF(svc *auth.Service) func(*ghttp.Request) bool {
	return func(r *ghttp.Request) bool {
		token := extractToken(r.Request)
		u, err := svc.Authenticate(r.Context(), token)
		if err != nil {
			commit(r, slog.Default(), http.StatusUnauthorized,
				nil, envelope.CodeUnauthorized, "登录已失效，请重新登录")
			return false
		}
		ctx := context.WithValue(r.Context(), ctxKeyUser, u)
		ctx = context.WithValue(ctx, ctxKeyToken, token)
		r.Request = r.Request.WithContext(ctx)
		return true
	}
}

// globalChainGF 返回挂"全局三层"的 GF 中间件（不接管响应序列化）。
//
// 参数顺序：log, afterNext, prepare, guardTarget, mws...
// 前三项都要显式写 nil —— 少写一个会让 recoverer 被当成 guardTarget，
// 既不生效还会被当成解码靶子。
func globalChainGF(log *slog.Logger) ghttp.HandlerFunc {
	return gfChain(log, nil, nil, nil,
		recoverer(log),
		requestLogger(log),
		securityHeaders,
	)
}

// responseChainGF 把"全局三层 + 响应序列化 + 请求体严格校验"
// 合成**一条** GF 中间件。
//
// ## 为什么必须是一条
//
// 响应序列化（writeBareResult）必须发生在 gfChain 链底、
// writer 替换的 defer 恢复之前，否则 statusWriter 已摘掉、
// GF 的 BytesWritten 也不累加，日志与响应都会失真。
// 做成独立中间件无法保证这个位置。
//
// reqFactory 是每请求新建 Req 实例的工厂，用于拒绝未知字段。
//
// 必须是工厂而不是实例：控制器/中间件是单例，共享 *Req 会被并发
// json.Decode 写入（数据竞争），并把残值带到下一个请求。
func responseChainGF(log *slog.Logger, d Deps, sessionTTL time.Duration, reqFactory func() any) ghttp.HandlerFunc {
	return responseChainGFWith(log, d, sessionTTL, reqFactory, nil)
}

// gate 是**原生 GF 形态**的前置守卫（可能提前拒绝）。
//
// 与标准库形态的区别：它拿得到 *ghttp.Request，因此能经 commit
// 一次写完 GF 状态与响应，不会让 GF 的 handleResponse 再补状态文本。
//
// 返回 false 表示已提前响应，链不再继续。
type gate func(*ghttp.Request) bool

// privResponseChainGF 在全局三层之后追加路由中间件。
//
// gates 是原生 GF 形态的守卫（authenticator 这类可能提前拒绝的），
// 排在 extra（标准库形态、只观察不提前响应的 audit 等）之前——
// 顺序与前缀路由一致：authenticator → audit。
func privResponseChainGF(
	log *slog.Logger,
	d Deps,
	sessionTTL time.Duration,
	reqFactory func() any,
	gates []gate,
	extra []middlewareFunc,
) ghttp.HandlerFunc {
	wrapped := make([]middlewareFunc, 0, len(gates)+len(extra))
	for _, g := range gates {
		g := g
		wrapped = append(wrapped, func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gfReq := ghttp.RequestFromCtx(r.Context())
				if gfReq == nil {
					// 拿不到 GF 请求说明链路装配出了问题。
					// 认证类守卫必须 fail closed：放行会让 logout 等
					// 有副作用的接口在未认证状态下执行。
					http.Error(w, "Hub 内部错误", http.StatusInternalServerError)
					return
				}

				// gate 在链底之前执行，此时 GF writer 的底层还是原始
				// *http.response（实测确认），而不是外层的 statusWriter。
				// 不接上 w 的话，commit 写出后 requestLogger 记到的会是
				// 默认的 200。这里仅在守卫执行期间替换，随后立即恢复，
				// 让链底按原逻辑再替换一次。不会自引用：w 底层是
				// net/http 原始 writer，不是 GF writer。
				sw := gfReq.Response.Writer
				old := sw.ResponseWriter
				sw.ResponseWriter = w
				passed := func() (ok bool) {
					defer func() { sw.ResponseWriter = old }()
					return g(gfReq)
				}()
				if !passed {
					return
				}
				// 守卫改写了 r.Request（放入用户信息），后续必须用新的。
				next.ServeHTTP(w, gfReq.Request)
			})
		})
	}
	wrapped = append(wrapped, extra...)
	return responseChainGFWith(log, d, sessionTTL, reqFactory, wrapped)
}

// responseChainGFWith 是两条链的共同实现。
func responseChainGFWith(
	log *slog.Logger,
	d Deps,
	sessionTTL time.Duration,
	reqFactory func() any,
	extra []middlewareFunc,
) ghttp.HandlerFunc {
	mws := []middlewareFunc{
		recoverer(log),
		requestLogger(log),
		securityHeaders,
	}
	mws = append(mws, extra...)
	return gfChain(log,
		func(r *ghttp.Request) { writeBareResult(r, d) },
		func(ctx context.Context) context.Context { return withSessionTTL(ctx, sessionTTL) },
		reqFactory,
		mws...,
	)
}
