package handler

import (
	"context"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// newRuntimeInfoHandler 处理 runtime.info。
//
// 作用：让 Hub 与本地 CLI 都能随时问"这台机器的引擎是什么、什么版本、是否 rootless"，
// 而不必等下一次 hello。基础操作排查里这是第一个要看的信息。
func newRuntimeInfoHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, _ []byte) (any, error) {
		info, err := rt.Info(ctx)
		if err != nil {
			return nil, err
		}
		return envelope.RuntimeInfo{
			Kind:     toKind(info.Kind),
			Version:  info.Version,
			APIVer:   info.APIVer,
			Socket:   info.Endpoint,
			Rootless: info.Rootless,
		}, nil
	}
}

// toKind 把运行时层的 Kind 映射为协议层的 RuntimeKind。
//
// 两个类型刻意保持分离（运行时层不依赖协议包），因此这里做一次显式转换，
// 而不是让运行时层直接返回协议类型。未知值一律归为 none，避免把脏数据推给 Hub。
func toKind(k runtime.Kind) envelope.RuntimeKind {
	switch k {
	case runtime.KindDocker:
		return envelope.RuntimeDocker
	case runtime.KindPodman:
		return envelope.RuntimePodman
	default:
		return envelope.RuntimeNone
	}
}
