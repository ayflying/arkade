// Package httpx 是 Hub 的 HTTP 层：路由装配、中间件、JSON 编解码。
//
// 分层约定：
//   - handlers*.go 只做「取参数 → 调 service → 写响应」，不写业务判断；
//   - 所有错误都经 respondError 统一映射成 {"error":{"code","message"}}，
//     前端只依赖这一种错误结构（契约见 docs/05-decisions.md 与 web 侧实现）；
//   - 权限校验在中间件里完成，handler 内不再重复判断。
package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"arkade/protocol/envelope"
)

// 请求体大小上限。Hub 的 API 只接受小型 JSON，不接收大文件
// （镜像与卷走独立流式通道，见 03-tech-stack.md）。
const maxBodyBytes = 1 << 20 // 1 MiB

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

// respondSecretJSON 用于响应体含**一次性密钥**的接口。
//
// 除了 respondJSON 已有的 no-store，还额外加三条：
//
//	Pragma: no-cache         —— 兼容 HTTP/1.0 的中间代理（它们只认这个）；
//	Cache-Control: no-store  —— 覆盖所有现代缓存，且明确"不要存到磁盘"；
//	X-Content-Type-Options   —— 防止浏览器把 JSON 当 HTML 嗅探渲染。
//
// 为什么值得单开一个函数：这些头只对"含密钥的响应"是必须的，
// 而"必须"的理由是密钥一旦被浏览器缓存或中间代理留存，
// 就等价于把凭据写进了一个用户无法清除、我们也看不到的地方。
// 把它们和普通响应用同一个函数，将来很容易在重构时丢失。
func respondSecretJSON(w http.ResponseWriter, log *slog.Logger, status int, body any) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	respondJSON(w, log, status, body)
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
	status := statusForCode(code)
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

// decodeJSON 解析请求体，拒绝未知字段。
//
// DisallowUnknownFields 是刻意的：前端多传字段说明契约已漂移，
// 静默忽略会让两边长期不一致却不被发现。
func decodeJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return errors.New("请求体为空")
	}
	limited := io.LimitReader(r.Body, maxBodyBytes)
	dec := json.NewDecoder(limited)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	return nil
}
