package envelope

// payload_request_cancel.go：Hub → Agent 的取消通知载荷。

// RequestCancelPayload 指向要取消的在途请求。
//
// 只带请求 ID，不带方法名与任何业务参数：
//
//   - 沿用请求 ID 才能与 Hub 的 pending 表一一对应，Agent 也只需要一张
//     "请求 ID → cancel 函数" 的表；
//   - 不带业务参数是为了让取消消息本身足够小、无歧义——它只表达
//     "停止 ID 为 X 的那次处理"，任何业务判断都留在 Agent 侧的原请求里。
type RequestCancelPayload struct {
	// ID 是发起方（Hub）为该请求生成的唯一标识，等于请求信封的 ID。
	ID string `json:"id"`
}
