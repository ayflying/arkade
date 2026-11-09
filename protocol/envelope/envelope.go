// Package envelope 定义 Hub 与 Agent 之间的统一消息信封（协议版本 v1）。
//
// 设计要点：
//   - 请求与响应通过 ID 关联，响应永远由接收方按请求的 ID 回填；
//   - Type=event 用于 Agent 主动上报（心跳、状态变化、任务进度），不参与请求响应配对；
//   - 握手（agent.hello）走 request/response 而不是 event：Hub 需要把分配的 agent_id
//     回传给 Agent，event 没有应答通道，无法完成这一步；
//   - 信封只承载控制信令与进度，镜像层、卷数据等大文件走独立流式通道，
//     这一点在 03-tech-stack 中有明确记录，禁止把大对象塞进 Payload。
package envelope

import (
	"encoding/json"
	"fmt"
	"time"
)

// Version 是当前协议版本。Agent 建连时上报自己支持的版本，Hub 据此协商；
// 版本不一致时应拒绝或降级，而不是让未知字段被静默丢弃。
const Version = 1

// Type 是信封类型。
type Type string

const (
	TypeRequest  Type = "request"  // 发起调用，等待对应 ID 的 response
	TypeResponse Type = "response" // 对某个 request 的应答（成功或失败）
	TypeEvent    Type = "event"    // 单向上报，无需应答
	TypeAck      Type = "ack"      // 对事件或心跳的确认
)

// Envelope 是唯一对外传输结构。字段全部使用短名，减少长连接上的字节开销。
type Envelope struct {
	V       int             `json:"v"`        // 协议版本
	ID      string          `json:"id"`       // 消息唯一标识（uuid）
	Type    Type            `json:"type"`     // 消息类型
	Method  string          `json:"method"`   // 方法名，见 method.go；event 时可为空
	Target  string          `json:"target"`   // 目标，如 agent:<agent_id> 或 hub
	TS      int64           `json:"ts"`       // 发送方 Unix 秒
	Payload json.RawMessage `json:"payload"`  // 方法私有载荷
	Error   *Error          `json:"error"`    // 仅 response 携带；非空即失败
	ReplyTo string          `json:"reply_to"` // 仅 response 携带，指向请求 ID
}

// Error 是可跨进程传输的错误。
//
// 与 Go 的 error 不同，它必须是可序列化的固定结构，且 Code 要稳定——
// 前端与审计日志依赖 Code 做判断，Message 只用于人看，随时可能调整措辞。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	stdErr  error  // 仅本地排查用，不参与序列化；供 Error() 拼出原始原因
}

// NewError 构造一个可传输错误。
func NewError(code, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

// WrapError 在可传输错误上保留原始 Go 错误，便于 Hub/Agent 打日志时看到根因。
func WrapError(code, msg string, cause error) *Error {
	return &Error{Code: code, Message: msg, stdErr: cause}
}

// Error 实现 error 接口，让 *Error 可以直接在 Go 侧当错误返回。
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.stdErr != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.stdErr)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap 让 errors.Is/As 可以追溯到根因（跨进程传输后 stdErr 为空，返回 nil）。
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.stdErr
}

// 稳定的错误码。新增错误码时同步更新前端文案映射，不要在业务里拼魔法字符串。
const (
	CodeBadRequest    = "bad_request"    // 参数错误
	CodeUnauthorized  = "unauthorized"   // 未认证
	CodeForbidden     = "forbidden"      // 无权限
	CodeNotFound      = "not_found"      // 目标不存在
	CodeAgentOffline  = "agent_offline"  // Agent 不在线，拒绝下发
	CodeAgentBusy     = "agent_busy"     // Agent 正忙（如正在执行不可中断任务）
	CodeUnsupported   = "unsupported"    // 目标运行时或版本不支持该操作
	CodeTimeout       = "timeout"        // 超时（等待 Agent 响应超时）
	CodeRuntimeFailed = "runtime_failed" // 运行时执行失败，Message 带原因
	CodeInternal      = "internal"       // 服务端内部错误
)

// NewRequest 构造一个请求信封。ID 由调用方传入，方便调用处复用同一 ID 做超时清理。
func NewRequest(id, method, target string, payload any) (*Envelope, error) {
	return newWithPayload(TypeRequest, id, method, target, payload)
}

// NewResponse 构造成功响应。
func NewResponse(req *Envelope, payload any) (*Envelope, error) {
	env, err := newWithPayload(TypeResponse, req.ID, req.Method, req.Target, payload)
	if err != nil {
		return nil, err
	}
	env.ReplyTo = req.ID
	return env, nil
}

// NewErrorResponse 构造失败响应。err 为 nil 时折叠为 internal 错误，避免出现空错误响应。
func NewErrorResponse(req *Envelope, err *Error) *Envelope {
	if err == nil {
		err = NewError(CodeInternal, "未知错误")
	}
	return &Envelope{
		V:       Version,
		ID:      req.ID,
		Type:    TypeResponse,
		Method:  req.Method,
		Target:  req.Target,
		TS:      time.Now().Unix(),
		Payload: json.RawMessage("null"),
		Error:   err,
		ReplyTo: req.ID,
	}
}

// NewEvent 构造一个事件信封（无请求配对方）。
func NewEvent(id, method, target string, payload any) (*Envelope, error) {
	return newWithPayload(TypeEvent, id, method, target, payload)
}

func newWithPayload(t Type, id, method, target string, payload any) (*Envelope, error) {
	raw, err := marshalPayload(payload)
	if err != nil {
		return nil, err
	}
	return &Envelope{
		V:       Version,
		ID:      id,
		Type:    t,
		Method:  method,
		Target:  target,
		TS:      time.Now().Unix(),
		Payload: raw,
	}, nil
}

// marshalPayload 处理 nil 载荷，统一序列化为 JSON null 而不是空字节，
// 否则接收方 json.Unmarshal 会因为空输入报错。
func marshalPayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return json.RawMessage("null"), nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("序列化载荷失败: %w", err)
	}
	return raw, nil
}

// DecodePayload 将载荷解码到目标结构。载荷为 null 时直接返回 nil，调用方无需判空。
func DecodePayload(env *Envelope, out any) error {
	if len(env.Payload) == 0 || string(env.Payload) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Payload, out); err != nil {
		return fmt.Errorf("解析载荷失败(method=%s): %w", env.Method, err)
	}
	return nil
}

// Validate 校验信封的基本合法性，防止空 ID、未知类型、错误版本进入路由阶段。
func (e *Envelope) Validate() *Error {
	if e == nil {
		return NewError(CodeBadRequest, "信封为空")
	}
	if e.V != Version {
		return NewError(CodeUnsupported, fmt.Sprintf("不支持的协议版本: %d", e.V))
	}
	if e.ID == "" {
		return NewError(CodeBadRequest, "缺少消息 ID")
	}
	switch e.Type {
	case TypeRequest, TypeResponse, TypeEvent, TypeAck:
	default:
		return NewError(CodeBadRequest, fmt.Sprintf("未知消息类型: %s", e.Type))
	}
	if e.Type == TypeRequest && e.Method == "" {
		return NewError(CodeBadRequest, "请求缺少方法名")
	}
	return nil
}
