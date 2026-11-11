// Package httpx 是 Hub 的 HTTP 层：路由装配、中间件、JSON 编解码。
//
// 分层约定：
//   - API 声明契约，typed controller 调用 service/store/agentlink；
//   - 本包集中输出裸 JSON 和 {"error":{"code","message"}} 错误信封；
//   - 认证在中间件，按资源的权限与输入校验在 controller，保留错误优先级。
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"arkade/protocol/envelope"
)

// apiError 是统一错误响应体。
type apiError struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// respondJSON 写出 JSON 响应。编码失败时无法再改响应，只能记录日志。
func respondJSON(w http.ResponseWriter, log *slog.Logger, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	// API 响应一律不缓存：容器状态是实时数据，缓存会导致界面显示陈旧。
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if body == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Error("写入响应失败", "err", err)
	}
}

// respondError 是唯一的错误出口。
//
// 映射规则与前端契约严格对应：
//   - unauthorized → 401，前端路由守卫据此跳登录页；
//   - forbidden    → 403（已登录但无权限），前端展示提示而非跳登录；
//   - not_found    → 404；
//   - agent_offline/timeout/runtime_failed → 503，表示依赖不可用，前端可提示稍后重试；
//   - unsupported  → 501。
//
// 脱敏原则：只有真正的服务端内部错误（internal）才用兜底文案掩盖细节；
// Agent 返回的业务错误（如「未检测到 Docker/Podman」）必须原样透出，
// 否则运维看不到真实原因，只能干瞪着眼看日志。
func respondError(w http.ResponseWriter, log *slog.Logger, code, message string) {
	respondErrorCode(w, log, statusForCode(code), code, message)
}

// respondErrorCode 与 respondError 相同，但由调用方显式指定 HTTP 状态码。
//
// 存在的理由：typed controller 已经用 Req/Resp 结构精确表达了每个接口
// 的状态语义（例如登录失败固定 401、退出固定 204），不该再经过
// "code → status"的一次推断，否则两处映射会各自演化、逐渐不一致。
//
// 脱敏规则与 respondError 保持一致：internal 码一律替换为兜底文案。
func respondErrorCode(w http.ResponseWriter, log *slog.Logger, status int, code, message string) {
	if code == envelope.CodeInternal {
		log.Error("接口返回内部错误", "code", code, "message", message)
		message = "服务器内部错误，请联系管理员查看日志"
	}
	respondJSON(w, log, status, apiError{Error: errorBody{Code: code, Message: message}})
}

// statusForCode 把协议错误码映射到 HTTP 状态码。
//
// 除 protocol 包提供的协议码外，Hub 自己的业务语义码也在这里统一映射：
// conflict（唯一约束冲突、如用户名/组名重复）→ 409。前端需要区分
// 「参数不对(400)」与「名字被占了(409)」，两者的用户引导完全不同。
func statusForCode(code string) int {
	switch code {
	case envelope.CodeUnauthorized:
		return http.StatusUnauthorized
	case envelope.CodeForbidden:
		return http.StatusForbidden
	case envelope.CodeNotFound:
		return http.StatusNotFound
	case envelope.CodeBadRequest:
		return http.StatusBadRequest
	// conflict 有两层来源，但语义一致，故合并成一条：
	//   - Hub 自己的业务语义（用户名/组名重名，见 codeConflict 常量）；
	//   - 协议层的 "目标状态不允许该操作"（envelope.CodeConflict）。
	// 两者都是「原样重试永远失败，必须提示用户改条件」，映射 409 正确。
	// 协议层提供同名常量后这里只需列一次，重复 case 在 Go 里是编译错误。
	case envelope.CodeConflict:
		return http.StatusConflict
	case envelope.CodeAgentOffline, envelope.CodeTimeout, envelope.CodeAgentBusy, envelope.CodeRuntimeFailed:
		return http.StatusServiceUnavailable
	case envelope.CodeUnsupported:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}
