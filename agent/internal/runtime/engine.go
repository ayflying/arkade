package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// engine 是 Docker 与 Podman 共用的实现。
//
// 为什么共用一个实现：两者的容器端点（列表/启停/删除/日志）在 Docker 兼容层下
// 完全一致，唯一差别是「怎么判定自己是谁」（/version 的 Components 字段）与
// 「是否 rootless」。把这些差异做成探测阶段的小函数塞进同一个结构，比复制两份
// 几乎相同的代码安全得多——复制必然导致一边修了 bug 另一边忘修（本项目明确禁止）。
type engine struct {
	kind     Kind
	client   *client
	endpoint string // 探测时实际使用的端点
	rootless bool   // Podman 无 root 模式
	info     Info   // 探测阶段缓存的版本信息，避免每次心跳都重复请求 /version

	// hostRoot/hostMount：宿主机路径换算（见 Config.HostRoot）。
	// 两者都为空表示"本进程不在容器里，宿主路径就是本机路径"，此时不做任何换算。
	hostRoot  string
	hostMount string
}

// 编译期断言：engine 必须实现 Runtime。
var _ Runtime = (*engine)(nil)

// versionResp 是 /version 的响应。
//
// 只声明用得上的字段：Components 用来区分 Docker 与 Podman（Podman 会额外带
// "Podman Engine" 组件，Docker 带 "Engine"），其余字段忽略以避免引擎升级带来的漂移。
type versionResp struct {
	Version    string `json:"Version"`
	APIVersion string `json:"ApiVersion"`
	Components []struct {
		Name    string `json:"Name"`
		Version string `json:"Version"`
		Details struct {
			APIVersion string `json:"ApiVersion"`
		} `json:"Details"`
	} `json:"Components"`
}

// Info 实现 Runtime。返回探测阶段缓存的版本信息，不打网络请求。
func (e *engine) Info(_ context.Context) (Info, error) {
	return e.info, nil
}

// listResp 是 /containers/json 的响应元素。
type listResp struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	Created int64             `json:"Created"`
	State   string            `json:"State"`
	Status  string            `json:"Status"`
	Labels  map[string]string `json:"Labels"`
	Ports   []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
	// NetworkSettings 在列表响应里是 NetworkSettingsSummary，
	// **只有 Networks 一个字段**（没有 Ports，端口在顶层）。
	//
	// 这是唯一能在"一次列表调用"里拿到容器 IP 的地方。
	// 用指针是因为老版本/某些兼容实现可能整个字段缺失（Podman 早期版本）。
	NetworkSettings *struct {
		Networks map[string]*struct {
			IPAddress      string `json:"IPAddress"`
			GlobalIPv6Addr string `json:"GlobalIPv6Address"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	// HostConfig 在列表响应里只取 NetworkMode。
	//
	// 这是安全字段（"host"/"bridge"/"none"/网络名），不含任何挂载或权限细节。
	// 界面需要它来区分"host 模式本来就没有独立 IP"与"容器还没拿到 IP"——
	// 两种情况下 Networks 都可能为空，但用户的解读完全不同，
	// 前者正常、后者可能是故障。
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
	} `json:"HostConfig"`
}

// List 实现 Runtime。
func (e *engine) List(ctx context.Context, all bool) ([]Container, error) {
	param := "?all=0"
	if all {
		param = "?all=1"
	}
	var raw []listResp
	if err := e.client.getJSON(ctx, pathContainers+param, &raw); err != nil {
		return nil, e.translate(err)
	}
	out := make([]Container, 0, len(raw))
	for _, r := range raw {
		out = append(out, toContainer(r))
	}
	return out, nil
}

// toContainer 把运行时结构转换成抽象层的 Container。
func toContainer(r listResp) Container {
	c := Container{
		ID:          r.ID,
		Name:        firstContainerName(r.Names),
		Image:       r.Image,
		State:       r.State,
		Status:      r.Status,
		Labels:      r.Labels,
		CreatedUnix: r.Created,
	}
	for _, p := range r.Ports {
		c.Ports = append(c.Ports, Port{IP: p.IP, Private: p.PrivatePort, Public: p.PublicPort, Type: p.Type})
	}
	// 网络与地址：稳定排序，避免前端每次刷新顺序抖动。
	if r.NetworkSettings != nil {
		for name, n := range r.NetworkSettings.Networks {
			cn := ContainerNetwork{Name: name}
			if n != nil {
				cn.IPAddress = n.IPAddress
				cn.IPv6Address = n.GlobalIPv6Addr
			}
			c.Networks = append(c.Networks, cn)
		}
		sortContainerNetworks(c.Networks)
	}
	c.NetworkMode = r.HostConfig.NetworkMode
	return c
}

// sortContainerNetworks 按网络名排序。
//
// 遍历 map 的顺序是随机的；不排序的话同一个容器每次列表返回的网络顺序
// 都可能不同，前端做差异对比时会误判为"变更"。
func sortContainerNetworks(ns []ContainerNetwork) {
	sort.Slice(ns, func(i, j int) bool { return ns[i].Name < ns[j].Name })
}

// firstContainerName 取主容器名并去掉运行时的前导斜杠。
func firstContainerName(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}

// Start 实现 Runtime。
func (e *engine) Start(ctx context.Context, idOrName string) error {
	if err := validateRef(idOrName); err != nil {
		return err
	}
	return e.translate(e.client.post(ctx, containerPath(idOrName)+actionStart))
}

// Stop 实现 Runtime。
//
// force 的语义与运行时保持一致：force 时发 kill（SIGKILL）而不是带超时的 stop，
// 因为 stop 的 ?t= 只影响优雅退出等待时间，并不会"立即杀掉"卡死进程。
func (e *engine) Stop(ctx context.Context, idOrName string, force bool) error {
	if err := validateRef(idOrName); err != nil {
		return err
	}
	p := containerPath(idOrName)
	if force {
		return e.translate(e.client.post(ctx, p+actionKill))
	}
	return e.translate(e.client.post(ctx, p+actionStop+"?t="+strconv.Itoa(defaultStopTimeoutSec)))
}

// defaultStopTimeoutSec 是优雅停止的等待秒数。
//
// 停止与重启共用这一个值：重启的语义是"先优雅停再启"，
// 两者用不同的超时只会让行为变得难以解释。
const defaultStopTimeoutSec = 10

// Remove 已迁移到 container_ops.go 的 RemoveContainer。
//
// 保留这个说明而不是删掉整段：该方法曾经是"只有一个 force 参数"的版本，
// 无法表达 removeVolumes，且与 RemoveContainer 语义重叠容易被误用。
// 现在容器删除只有 RemoveContainer 一个入口，签名把两个危险开关都显式化。

// Logs 实现 Runtime。
//
// 运行时的 logs 响应有两种格式：非 TTY 容器是多路复用帧（stdout/stderr 交错，
// 每帧 8 字节头），TTY 容器是纯文本。这里先用 decodeLogPayload 统一解帧，
// 再按行切分——直接把响应当字符串会把帧头字节混进输出，前端会看到乱码。
// 行数上限由调用方决定，运行时不扩大它。
func (e *engine) Logs(ctx context.Context, idOrName string, opts LogOptions) (LogResult, error) {
	if err := validateRef(idOrName); err != nil {
		return LogResult{}, err
	}
	tail := opts.TailLines
	if tail <= 0 {
		tail = defaultTailLines
	}
	parts := []string{
		"follow=" + logQueryFollow,
		logQueryTailPrefix + strconv.Itoa(tail),
	}
	if opts.Stdout {
		parts = append(parts, logQueryStdout)
	}
	if opts.Stderr {
		parts = append(parts, logQueryStderr)
	}
	if opts.Timestamps {
		parts = append(parts, logQueryTimestamps)
	}
	data, err := e.client.do(ctx, "GET", containerPath(idOrName)+actionLogs+"?"+strings.Join(parts, "&"), nil)
	if err != nil {
		return LogResult{}, e.translate(err)
	}
	lines := splitLogLines(decodeLogPayload(data))
	lines, truncated := trimTail(lines, tail)
	return LogResult{Lines: lines, Truncated: truncated}, nil
}

// defaultTailLines 是调用方未指定行数时的默认值，防止无界拉取。
const defaultTailLines = 200

// containerPath 生成容器端点路径。
//
// 用 url.PathEscape 处理名字里的特殊字符：正常容器名只含点、下划线、连字符，
// 统一转义可以避免把未预期的内容注入成别的端点路径。
func containerPath(idOrName string) string {
	return pathContainerPrefix + url.PathEscape(idOrName)
}

// validateRef 拒绝空引用与包含路径分隔符的引用。
func validateRef(idOrName string) error {
	s := strings.TrimSpace(idOrName)
	if s == "" {
		return errors.New("容器 ID 或名称不能为空")
	}
	if strings.ContainsAny(s, "/\\") {
		return fmt.Errorf("容器 ID 或名称非法: %q", idOrName)
	}
	return nil
}

// translate 把客户端层的 HTTP 状态错误翻译成抽象层错误。
//
// 统一在这里做映射，保证上层只看到 ErrNotFound 或其他包装错误，不感知
// Docker/Podman 的差异化错误文案。404 映射为 ErrNotFound，因为 Agent 只用
// 这几个容器端点，404 只可能意味着容器不存在。
func (e *engine) translate(err error) error {
	if err == nil {
		return nil
	}
	var se *statusError
	if errors.As(err, &se) && se.Status == 404 {
		return fmt.Errorf("%w: %s", ErrNotFound, se.Body)
	}
	return err
}
