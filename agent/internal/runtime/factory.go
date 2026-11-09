package runtime

import (
	"context"
	"fmt"
	"strings"
)

// Config 是创建运行时的配置。
//
// 只放"选择哪个端点"这一件事：引擎类型不靠配置声明，而是探测出来的。
// 原因是用户经常记不清自己机器上是 Docker 还是 Podman（尤其机器是别人装的），
// 让程序自己判定比让人填错再报错更可靠。
type Config struct {
	// Socket 为显式端点覆盖（unix 路径、unix://…、tcp://host:port）。
	// 留空则按 discover.go 的优先级自动发现。
	Socket string
	// PreferPodman 在自动发现时优先尝试 Podman 套接字。
	// 注意它只影响"先试谁"，最终引擎类型以探测结果为准。
	PreferPodman bool
}

// New 按配置创建运行时。
//
// 返回约定（工厂的硬性契约）：
//   - 找到可用运行时：返回具体实现，err 为 nil；
//   - 一个都连不上：返回 RuntimeNone 且 err 为 nil——这是"环境事实"而非程序错误，
//     Agent 需要带着 RuntimeNone 继续连 Hub，好让运维在界面上看到"这台机器没装引擎"；
//   - 用户显式配置了端点却探测失败：返回 RuntimeNone 且 err 非 nil，避免静默降级
//     成"看起来没装引擎"这种误导性状态。
func New(cfg Config) (Runtime, error) {
	endpoints := discoverEndpoints(cfg.Socket)
	if cfg.PreferPodman && strings.TrimSpace(cfg.Socket) == "" {
		endpoints = preferPodmanFirst(endpoints)
	}

	var lastErr error
	for _, ep := range endpoints {
		// unix 套接字文件不存在时直接跳过，避免每次启动都在不存在的路径上等超时。
		if !endpointExists(ep) {
			continue
		}
		rt, err := probe(context.Background(), ep, cfg.PreferPodman)
		if err == nil {
			return rt, nil
		}
		lastErr = err
	}

	// 显式配置的端点探测失败必须报错：用户指定了目标却没连上，属于配置问题，
	// 静默回落到 RuntimeNone 会让人误以为这台机器没装引擎。
	if strings.TrimSpace(cfg.Socket) != "" {
		if lastErr == nil {
			lastErr = fmt.Errorf("端点不存在或不可访问: %s", cfg.Socket)
		}
		return NewNone(""), fmt.Errorf("按配置探测运行时失败: %w", lastErr)
	}
	return NewNone(""), nil
}

// preferPodmanFirst 把 podman 相关端点提到最前面，其余保持原序。
func preferPodmanFirst(endpoints []string) []string {
	first := make([]string, 0, len(endpoints))
	rest := make([]string, 0, len(endpoints))
	for _, ep := range endpoints {
		if strings.Contains(ep, "podman") {
			first = append(first, ep)
			continue
		}
		rest = append(rest, ep)
	}
	return append(first, rest...)
}
