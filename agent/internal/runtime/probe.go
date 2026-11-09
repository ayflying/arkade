package runtime

import (
	"context"
	"fmt"
	"strings"
)

// probe 对一个端点做「连得上 → 探测版本 → 判定引擎类型」三步。
//
// 顺序有讲究：
//  1. 先打 /version：它同时返回版本号与组件信息，一次往返就能完成版本探测与类型判定，
//     比先 ping 再 version 少一次 RTT；
//  2. 用 Components 判定引擎：Podman 的 compat 层会额外返回 "Podman Engine" 组件，
//     而 Docker 返回 "Engine"。只靠版本号字符串判定不可靠（两者版本号可能撞号），
//     只靠套接字路径判定更不可靠（用户可能把 podman 套接字软链到 docker.sock）；
//  3. 判定不出来时按套接字路径兜底，最后再按 preferPodman 决定。
func probe(ctx context.Context, endpoint string, preferPodman bool) (Runtime, error) {
	c, err := newClient(endpoint)
	if err != nil {
		return nil, err
	}

	var v versionResp
	if err := c.getJSON(ctx, pathVersion, &v); err != nil {
		return nil, fmt.Errorf("探测 %s 失败: %w", endpoint, err)
	}

	kind := detectKind(endpoint, v, preferPodman)
	if kind == KindNone {
		// /version 通了但认不出是谁：这可能是极老版本或是代理层，
		// 此时不做兜底猜测，交给调用方继续试下一个端点。
		return nil, fmt.Errorf("端点 %s 可访问但无法判定运行时类型（version=%q）", endpoint, v.Version)
	}

	e := &engine{
		kind:     kind,
		client:   c,
		endpoint: endpoint,
		rootless: kind == KindPodman && isRootless(endpoint),
	}
	e.info = Info{
		Kind:     kind,
		Version:  v.Version,
		APIVer:   v.APIVersion,
		Endpoint: endpoint,
		Rootless: e.rootless,
	}
	return e, nil
}

// detectKind 判定引擎类型。
//
// 判定优先级：/version 的组件 → 套接字路径 → 用户偏好。
func detectKind(endpoint string, v versionResp, preferPodman bool) Kind {
	for _, comp := range v.Components {
		switch {
		case strings.Contains(strings.ToLower(comp.Name), "podman"):
			return KindPodman
		}
	}
	if strings.Contains(strings.ToLower(endpoint), "podman") {
		return KindPodman
	}
	if strings.Contains(strings.ToLower(endpoint), "docker") {
		return KindDocker
	}
	// 组件里出现 Engine（Docker 的固定组件名）时判定为 Docker。
	for _, comp := range v.Components {
		if strings.EqualFold(comp.Name, "Engine") {
			return KindDocker
		}
	}
	// /version 至少返回了版本号，说明它就是一个 Docker 兼容守护进程；
	// 认不出具体是谁时尊重用户偏好，默认按 Docker 处理。
	if v.Version != "" {
		if preferPodman {
			return KindPodman
		}
		return KindDocker
	}
	return KindNone
}

// isRootless 判断 Podman 是否为 rootless 模式。
//
// 判定依据是套接字所在位置：rootless 的套接字一定落在用户运行时目录下
// （$XDG_RUNTIME_DIR 或 /run/user/<uid>），root 模式在 /run/podman 下。
// 这个差异会影响后续卷与端口行为（rootless 无法绑定 <1024 端口），因此必须如实上报。
func isRootless(endpoint string) bool {
	ep := strings.ToLower(endpoint)
	if strings.Contains(ep, "/run/user/") {
		return true
	}
	if strings.Contains(ep, "xdg_runtime_dir") {
		return true
	}
	return false
}

// noneRuntime 是"没有可用运行时"时的实现。
//
// 存在的意义是让上层代码不需要到处判空：所有方法都返回 ErrNone，处理器会把它
// 翻译成协议里的 unsupported 错误，CLI 会打印"本机没有检测到运行时"。
// 这种空对象实现比让工厂返回 nil 更安全，也比让每个调用点都写 if rt == nil 更简洁。
type noneRuntime struct {
	endpoint string
}

// 编译期断言：noneRuntime 必须实现 Runtime。
var _ Runtime = (*noneRuntime)(nil)

// NewNone 构造一个"无运行时"实现。
func NewNone(endpoint string) Runtime {
	return &noneRuntime{endpoint: endpoint}
}

// Info 返回 KindNone，供 hello 上报。
func (n *noneRuntime) Info(_ context.Context) (Info, error) {
	return Info{Kind: KindNone, Endpoint: n.endpoint}, nil
}

// List 无运行时可直接返回空列表——这不是错误，而是"本机没有容器"的事实，
// 让 Hub 侧看到空列表比看到一个错误更容易理解。
func (n *noneRuntime) List(_ context.Context, _ bool) ([]Container, error) {
	return nil, nil
}

// Start 返回 ErrNone。
func (n *noneRuntime) Start(_ context.Context, _ string) error { return ErrNone }

// Stop 返回 ErrNone。
func (n *noneRuntime) Stop(_ context.Context, _ string, _ bool) error { return ErrNone }

// Remove 返回 ErrNone。
func (n *noneRuntime) Remove(_ context.Context, _ string, _ bool) error { return ErrNone }

// Logs 返回 ErrNone。
func (n *noneRuntime) Logs(_ context.Context, _ string, _ LogOptions) (LogResult, error) {
	return LogResult{}, ErrNone
}

// ListImages 无运行时返回空列表。
//
// 与 List 同理：列出"没有镜像"比报错更容易理解，也让界面能正常渲染空态。
// 但**删除类操作必须返回 ErrNone**——静默成功会误导用户以为已经删掉了。
func (n *noneRuntime) ListImages(_ context.Context) ([]Image, error) { return nil, nil }

// RemoveImage 返回 ErrNone。
func (n *noneRuntime) RemoveImage(_ context.Context, _ string, _ bool) error { return ErrNone }

// ListNetworks 无运行时返回空列表。
func (n *noneRuntime) ListNetworks(_ context.Context) ([]Network, error) { return nil, nil }

// RemoveNetwork 返回 ErrNone。
func (n *noneRuntime) RemoveNetwork(_ context.Context, _ string) error { return ErrNone }

// ListVolumes 无运行时返回空列表。
func (n *noneRuntime) ListVolumes(_ context.Context) ([]Volume, error) { return nil, nil }

// RemoveVolume 返回 ErrNone。
func (n *noneRuntime) RemoveVolume(_ context.Context, _ string) error { return ErrNone }

// IsNone 判断是否为"无运行时"实现，供上层快速分流。
//
// 用类型断言而不是 errors.As：Runtime 是业务接口，不实现 error，
// 这里比较的是"实现类型"而不是错误链。
func IsNone(rt Runtime) bool {
	if rt == nil {
		return true
	}
	_, ok := rt.(*noneRuntime)
	return ok
}
