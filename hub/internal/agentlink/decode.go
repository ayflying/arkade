package agentlink

// decode.go 是载荷解码的统一出口，把解码错误翻译成协议错误码。

import (
	"encoding/json"

	"arkade/protocol/envelope"
)

// decodeInto 把 Agent 返回的原始载荷解析到目标结构。
//
// 错误处理原则：Agent 返回了内容但结构不对，属于运行时不兼容（例如
// Agent 版本过旧、字段被改写），应返回 unsupported 而不是 internal，
// 这样运维能立刻定位到「升级 Agent」的方向。
func decodeInto(raw []byte, out any) *ConnError {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &ConnError{
			Code:    envelope.CodeUnsupported,
			Message: "Agent 返回的数据结构无法解析，可能是 Agent 版本过旧",
			Wrapped: err,
		}
	}
	return nil
}
