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
		// ErrNotFound 的原始文案是"容器不存在"，但资源类接口（镜像/网络/卷）
		// 会先用 %w 包上自己的说明，这里优先用包装后的文本，才能说清是哪类资源。
		msg := "容器不存在或已被删除"
		if s := err.Error(); s != runtime.ErrNotFound.Error() {
			msg = s
		}
		return envelope.WrapError(envelope.CodeNotFound, msg, err)
	case errors.Is(err, errBadRequest):
		// 参数错误的 Message 已经是完整中文说明，不再加前缀
		// （badRequestf 生成的是"%w: 说明"，去掉哨兵原文更干净）。
		return envelope.WrapError(envelope.CodeBadRequest, stripSentinel(err, errBadRequest), err)
	case errors.Is(err, runtime.ErrUnsupported):
		return envelope.WrapError(envelope.CodeUnsupported, err.Error(), err)
	case errors.Is(err, errErrComposeFailed):
		// 编排命令失败：责任在"被管理的机器"上（端口冲突、镜像拉不到、
		// YAML 写错），映射成 runtime_failed 而不是 internal，
		// 便于运维区分"用户配置问题"与"Agent 程序 bug"。
		// 文案原样保留——里面带着 compose 的真实输出，那才是用户要看的。
		return envelope.WrapError(envelope.CodeRuntimeFailed, stripSentinel(err, errErrComposeFailed), err)
	}

	// 其余一律视为运行时执行失败：套接字连不上、引擎返回 5xx、权限不足等。
	// 不把它折叠成 internal，是因为这类错误的责任在"被管理的机器"而不是 Agent 自身。
	return envelope.WrapError(envelope.CodeRuntimeFailed, fmt.Sprintf("运行时操作失败: %v", err), err)
}

// stripSentinel 去掉错误文本里的哨兵前缀。
//
// fmt.Errorf("%w: xxx", sentinel) 生成的文本是"哨兵原文: xxx"，
// 而哨兵原文（如"参数错误"）本身对用户没有价值，只需要后半截。
// 用 TrimPrefix 而不是字符串切割：前缀不存在时原样返回，不会误伤。
func stripSentinel(err error, sentinel error) string {
	s := err.Error()
	prefix := sentinel.Error() + ": "
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

// errBadRequest 是参数类错误的哨兵，便于在上面统一映射。
var errBadRequest = errors.New("参数错误")

// badRequestf 构造带上下文的参数错误。
func badRequestf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errBadRequest, fmt.Sprintf(format, args...))
}
