package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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
	return c
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
const defaultStopTimeoutSec = 10

// Remove 实现 Runtime。
func (e *engine) Remove(ctx context.Context, idOrName string, force bool) error {
	if err := validateRef(idOrName); err != nil {
		return err
	}
	p := containerPath(idOrName)
	if force {
		p += "?force=1"
	}
	return e.translate(e.client.delete(ctx, p))
}

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
