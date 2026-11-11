package httpx

// middleware_bare_json.go 把 GoFrame 规范控制器的返回值按 Hub 契约写出。
//
// ## 与 ghttp.MiddlewareHandlerResponse 的关系
//
// GF 的默认策略（ghttp_middleware_handler_response.go:36）是：
//
//	r.Middleware.Next()
//	err = r.GetError()
//	res = r.GetHandlerResponse()
//	→ WriteJson(DefaultHandlerResponse{Code,Message,Data})
//
// 我们复制的是**这个策略的结构**，只换掉最后一步的序列化：
//
//	Next() → GetError/GetHandlerResponse → 按 Hub 裸 JSON 写
//
// ## 位置约束（决定了它不是中间件）
//
// 这段逻辑必须在 gfChain **链底**、`Next()` 返回后、
// writer 替换的 defer 恢复**之前**执行。做成独立中间件会两处失真：
// 被包在 mws 外层、且链底 defer 已把 writer 恢复，
// 于是 statusWriter 记不到状态、GF 的 BytesWritten 也不累加。
//
// 所以它是普通函数，由链底直接调用。

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/gogf/gf/v2/net/ghttp"

	"arkade/hub/internal/controller"
)

// writeBareResult 在链底写出控制器结果。
//
// 调用时机：GF 的 `Next()` 已返回（Res/error 就绪），
// 且 `r.Response.Writer` 的底层仍是最外层包装 writer。
func writeBareResult(r *ghttp.Request, d Deps) {
	// 已经写过响应就不要再写。
	//
	// 两个来源：
	//  1. 下游自己写了（流式下载）：BufferLength/BytesWritten 非 0；
	//  2. 链上守卫（authenticatorGF）提前拒绝并经 commit 写完：
	//     commit 会把 committed 标记设在 *ghttp.Request 上。
	//
	// 不能只看 Response.Status：GF 会在请求生命周期里写入该字段，
	// 用它判断会把正常请求也跳过，导致"响应为空"。
	if errors.Is(r.GetError(), controller.ErrResponseCommitted) {
		r.SetError(nil)
		markCommitted(r)
		return
	}
	if r.Response.BufferLength() > 0 || r.Response.BytesWritten() > 0 || isCommitted(r) {
		return
	}
	if err := r.GetError(); err != nil {
		writeBareError(r, d, err)
		return
	}

	res := r.GetHandlerResponse()
	if res == nil {
		// 成功但无返回体：204，避免写出空的 200 body。
		commit(r, d.Log, http.StatusNoContent, nil, "", "")
		return
	}
	commit(r, d.Log, controller.ResponseStatus(r), res, "", "")
}

// commit 是唯一的响应写出口。
//
// ## 为什么写 r.Response.Writer 而不是中间件收到的 w
//
// `r.Response.Writer` 是 GF 的 `*response.Writer`，其内嵌的
// ResponseWriter 已被链底换成**最外层包装**（statusWriter 等）。
// 因此经它写出会：
//
//  1. 走完标准库链（requestLogger 能记状态与大小）；
//  2. 让 GF 的 bytesWritten 正常累加。
//
// 若直接写中间件手里的 `w`，就绕过了 `*response.Writer`：
// GF 侧 BytesWritten 恒为 0、Status 为 0，随后
// `BufferWriter.Flush` 在"非 200 + buffer 空 + bytes 0"时补写状态文本
// （实测表现为 body 尾部多出 "\nUnauthorized"）。
//
// ## 为什么 SetError(nil)
//
// handleResponse 在 Status==0 且 GetError() 非空时会把 err.Error()
// 写进 body。我们已写完响应，必须清掉错误，避免二次输出。
func commit(r *ghttp.Request, log *slog.Logger, status int, body any, code, message string) {
	r.Response.Status = status
	switch {
	case body != nil:
		respondJSON(r.Response.Writer, log, status, body)
	case code != "":
		respondErrorCode(r.Response.Writer, log, status, code, message)
	default:
		r.Response.WriteHeader(status)
	}
	// 清除错误：避免 handleResponse 把 err.Error() 当响应体写出。
	r.SetError(nil)
	// 标记"已由我们写完"，供链底判断是否需要再写。
	markCommitted(r)
}

// committedKey 是 *ghttp.Request 上的 "响应已写完" 标记键。
//
// 为什么需要它：链上守卫（authenticatorGF）提前拒绝时会经 commit
// 写完响应，但控制流随后仍可能回到链底。链底必须能区分
// "已经写过" 与 "还没写"，否则会写出第二个 JSON body。
//
// 不用 Response.Status 判断：GF 会在请求生命周期里写入该字段，
// 用它判断会把正常请求也跳过。
const committedKey = "httpx_committed"

// markCommitted 标记该请求的响应已写完。
func markCommitted(r *ghttp.Request) {
	r.SetCtxVar(committedKey, true)
}

// isCommitted 判断响应是否已由 commit 写完。
func isCommitted(r *ghttp.Request) bool {
	v := r.GetCtxVar(committedKey)
	return v != nil && v.Bool()
}

// writeBareError 把错误按 Hub 的错误信封写出。
func writeBareError(r *ghttp.Request, d Deps, err error) {
	// 无正文的成功结果（退出登录 204）。
	if errors.Is(err, controller.ErrNoContent) {
		commit(r, d.Log, http.StatusNoContent, nil, "", "")
		return
	}
	// 控制器显式声明的业务错误：状态与 code 都由它给出。
	var ce *controller.Error
	if errors.As(err, &ce) {
		if ce.Err() != nil {
			d.Log.Error("接口返回业务错误", "code", ce.Code, "err", ce.Err())
		}
		commit(r, d.Log, ce.Status, nil, ce.Code, ce.Message)
		return
	}
	// 未归类错误一律按内部错误处理，绝不把原始 err 写进响应。
	d.Log.Error("接口返回未归类错误", "err", err)
	commit(r, d.Log, http.StatusInternalServerError, nil, "internal", "服务器内部错误")
}
