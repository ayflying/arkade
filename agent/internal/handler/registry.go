// Package handler 是 Agent 的方法注册表与具体处理器实现。
//
// 关键设计（决策 D-05）：在线通道（Hub 下行请求）与本地 CLI（arkade-agent ps/start/...）
// 共用同一个 Registry。CLI 并不另写一套"读本地 Docker"的逻辑，而是构造一个和 Hub
// 下发的完全一样的 envelope，交给同一个 Dispatch 执行。这样保证：
//   - 离线自治行为与在线行为逐字节一致；
//   - 只有一处需要维护参数校验与错误翻译，不存在"线上修了、离线忘了"的分裂。
package handler

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// Handler 处理方法调用并返回待序列化的载荷。
//
// payload 是请求信封的原始 JSON 载荷（可能为 null），由各处理器自行解码，
// 这样 Registry 不需要知道每种方法的载荷类型。
type Handler func(ctx context.Context, payload []byte) (any, error)

// Registry 是方法名 → 处理器的映射表。
//
// 并发说明：注册只在启动阶段完成（单线程），运行期只读，因此用普通 map 即可；
// 仍加 RWMutex 是为了防御未来可能的动态注册（如自升级后热加载能力），
// 让"读多写少"的语义显式可见。
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

// Register 注册一个方法。重复注册直接覆盖并在语义上视为配置错误——
// 但这里不做 panic：工具函数可能被多条初始化路径调用，覆盖反而更安全。
func (r *Registry) Register(method string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.handlers[method] = h
}

// Has 判断方法是否已注册，用于能力清单生成。
func (r *Registry) Has(method string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.handlers[method]
	return ok
}

// Methods 返回已注册方法名（排序后），用于 hello 的能力上报。
// 排序是为了让日志与测试输出稳定，避免 map 遍历顺序带来的抖动。
func (r *Registry) Methods() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.handlers))
	for m := range r.handlers {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// Dispatch 执行一个请求信封，返回响应信封。
//
// 约定：无论处理器是否出错，都返回一个 response 信封（失败时带 Error 字段），
// 绝不返回 nil——调用方（Hub 或 CLI）依赖"每个请求都有回应"来避免永久等待。
func (r *Registry) Dispatch(ctx context.Context, req *envelope.Envelope) *envelope.Envelope {
	if req == nil {
		// 没有请求就没有可回填的 ID，只能构造一个独立的错误响应。
		return envelope.NewErrorResponse(&envelope.Envelope{ID: "unknown"}, envelope.NewError(envelope.CodeBadRequest, "请求为空"))
	}
	if e := req.Validate(); e != nil {
		return envelope.NewErrorResponse(req, e)
	}
	if req.Type != envelope.TypeRequest {
		return envelope.NewErrorResponse(req, envelope.NewError(envelope.CodeBadRequest,
			fmt.Sprintf("调度期望 request 类型，收到 %s", req.Type)))
	}

	r.mu.RLock()
	h, ok := r.handlers[req.Method]
	r.mu.RUnlock()
	if !ok {
		// 未注册的方法通常是版本不匹配（Hub 比 Agent 新），错误码用 unsupported
		// 而不是 not_found，便于 Hub 侧提示"请升级 Agent"。
		return envelope.NewErrorResponse(req, envelope.NewError(envelope.CodeUnsupported,
			fmt.Sprintf("Agent 不支持方法 %q（可能是 Agent 版本过旧，请升级）", req.Method)))
	}

	result, err := h(ctx, req.Payload)
	if err != nil {
		return envelope.NewErrorResponse(req, toEnvelopeError(err))
	}
	resp, merr := envelope.NewResponse(req, result)
	if merr != nil {
		return envelope.NewErrorResponse(req, envelope.NewError(envelope.CodeInternal,
			"序列化响应失败: "+merr.Error()))
	}
	return resp
}

// RegisterDefaults 把阶段一的方法一次性注册到注册表。
//
// 之所以用"一个注册函数"而不是让调用方逐个 Register：方法集合与能力清单
// 必须严格对应，集中注册可以保证 hello 上报的能力与实际能处理的方法不会脱节。
func RegisterDefaults(reg *Registry, rt runtime.Runtime) {
	reg.Register(envelope.MethodRuntimeInfo, newRuntimeInfoHandler(rt))
	reg.Register(envelope.LocalMethodContainerList, newListHandler(rt))
	reg.Register(envelope.LocalMethodContainerStart, newStartHandler(rt))
	reg.Register(envelope.LocalMethodContainerStop, newStopHandler(rt))
	reg.Register(envelope.LocalMethodContainerLogs, newLogsHandler(rt))
}
