package handler

import (
	"errors"
	"fmt"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// toEnvelopeError 把内部 Go 错误翻译成可跨进程传输的 *envelope.Error。
//
// 这一层是"内部错误模型"与"协议错误模型"的唯一桥梁：
//   - 运行时层只返回 ErrNone / ErrNotFound 等语义错误，不懂协议错误码；
//   - 处理器只做参数校验并原样向上抛；
//   - 翻译集中在这里，保证同一个语义在所有方法上映射成同一个错误码，
//     不会出现"list 返回 unsupported、logs 却返回 runtime_failed"的不一致。
//
// 注意 Message 是给人看的（前端直接展示），错误码才是稳定契约。
func toEnvelopeError(err error) *envelope.Error {
	if err == nil {
		return nil
	}

	// 已经是协议错误（例如处理器内直接构造）时原样透传。
	var ee *envelope.Error
	if errors.As(err, &ee) {
		return ee
	}

	switch {
	case errors.Is(err, runtime.ErrNone):
		return envelope.WrapError(envelope.CodeUnsupported,
			"本机未检测到可用的容器运行时（Docker / Podman），请先在服务器上安装并启动其一", err)
	case errors.Is(err, runtime.ErrNotFound):
		return envelope.WrapError(envelope.CodeNotFound, "容器不存在或已被删除", err)
	case errors.Is(err, errBadRequest):
		return envelope.WrapError(envelope.CodeBadRequest, err.Error(), err)
	case errors.Is(err, runtime.ErrUnsupported):
		return envelope.WrapError(envelope.CodeUnsupported, err.Error(), err)
	}

	// 其余一律视为运行时执行失败：套接字连不上、引擎返回 5xx、权限不足等。
	// 不把它折叠成 internal，是因为这类错误的责任在"被管理的机器"而不是 Agent 自身。
	return envelope.WrapError(envelope.CodeRuntimeFailed, fmt.Sprintf("运行时操作失败: %v", err), err)
}

// errBadRequest 是参数类错误的哨兵，便于在上面统一映射。
var errBadRequest = errors.New("参数错误")

// badRequestf 构造带上下文的参数错误。
func badRequestf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadRequest, fmt.Sprintf(format, args...))
}
