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
	// HostRoot 是"容器内的宿主机根目录"，留空表示没有挂进来。
	//
	// ============================ 为什么需要它 ============================
	// Agent 自己是容器，看到的文件系统不是宿主机的。外部编排（宝塔等）的
	// 配置路径来自容器标签，是**宿主机**绝对路径；不换算成容器内路径，
	// 只能读到不存在的目录，表现为"读取编排配置失败"。
	//
	// 换算规则很短：宿主 <HostRoot>/x  →  容器内 <HostMount>/x。
	// 目标路径里出现 .. 、不是绝对路径、或不在 HostRoot 之下 → 不受理，
	// 让调用方按"读不到"如实报错，绝不猜一个相近路径。
	HostRoot string
	// HostMount 是 HostRoot 在**本进程容器内**的挂载点。
	// 只有 HostRoot 非空时才生效；默认见 DefaultHostMount。
	HostMount string
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
			// 宿主路径换算只在容器化部署（挂了宿主目录）时才配置；
			// 空值保持原样，原生部署的 Agent 行为完全不变。
			if e, ok := rt.(*engine); ok {
				e.hostRoot = cfg.HostRoot
				e.hostMount = cfg.HostMount
			}
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
