package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

// 容器来源标记（对应 01 架构 4.2 的 origin 字段）。
//
// 首期只区分 discovered 与本地登记表里的 arkade/adopted：运行时本身不记录
// "这个容器是谁创建的"，只有 Arkade 自己创建的容器才能确定归属。
// 未登记的容器一律按 discovered 处理（可展示、可基础操作），这是保守而安全的选择。
const (
	originArkade     = "arkade"
	originAdopted    = "adopted"
	originDiscovered = "discovered"
)

// shortIDLen 是列表展示用的短 ID 长度，与 docker CLI 习惯一致。
const shortIDLen = 12

// labelComposeProject 是 compose 自己写在容器上的项目名标签。
//
// 这里是**字面量**而不是引用 runtime/compose 包的常量：
// 那两处常量都是包内小写（外部不可见），为这一个字符串导出它们
// 会让两个包的可见面变大，收益却只有一行。该标签是 Docker/Compose
// 官方约定而非我们的发明，字面量是安全的。
const labelComposeProject = "com.docker.compose.project"

// listHandler 处理 container.list。
//
// 载荷：无（nil / null）。
// 应答：envelope.ContainerListResult。
func newListHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, _ []byte) (any, error) {
		// 始终列出全部容器（含已停止）：Hub 侧需要完整视图来做对账，
		// 只列运行中的容器会让"停止的容器"消失，用户会误以为它被删了。
		containers, err := rt.List(ctx, true)
		if err != nil {
			return nil, err
		}
		out := make([]envelope.ContainerSummary, 0, len(containers))
		for _, c := range containers {
			out = append(out, toSummary(c))
		}
		return envelope.ContainerListResult{Containers: out}, nil
	}
}

// toSummary 把运行时容器转换成协议摘要。
//
// 归一化动作集中在这里（短 ID、去斜杠、端口摘要、创建时间），保证 Hub 与 CLI
// 看到的是同一份数据形态，不需要前端各自实现一遍格式化。
func toSummary(c runtime.Container) envelope.ContainerSummary {
	return envelope.ContainerSummary{
		ID:     shortID(c.ID),
		Name:   c.Name,
		Image:  c.Image,
		State:  c.State,
		Status: c.Status,
		Origin: originDiscovered,
		// StackID 填 compose 项目名（不是 Hub 数据库里的 ID）。
		//
		// 用户反馈的真实缺口：外部项目的详情页需要按项目把所有容器
		// 归到一组，而 Hub 侧只有容器列表可用。这个值就是那个关联键。
		// 手工 docker run 起的容器没有该标签，保持空字符串。
		//
		// 只取这一个标签，**不搬运整个 Labels**：标签里可能含用户
		// 自定义的敏感信息，没有让它们进入协议的必要。
		StackID:        c.Labels[labelComposeProject],
		Created:        c.CreatedUnix,
		Ports:          formatPorts(c.Ports),
		PublishedPorts: toPublishedPorts(c.Ports),
		Networks:       toSummaryNetworks(c.Networks),
		NetworkMode:    c.NetworkMode,
	}
}

// toSummaryNetworks 把运行时网络信息转成协议结构（安全子集）。
//
// 只搬名字与两个地址。空地址**原样保留为空**：
// host 模式与 network_mode: none 的容器本来就没有 IP，
// 补成 0.0.0.0 或宿主机地址等于编造一个不存在的可访问地址。
func toSummaryNetworks(ns []runtime.ContainerNetwork) []envelope.ContainerNetwork {
	if len(ns) == 0 {
		return nil
	}
	out := make([]envelope.ContainerNetwork, 0, len(ns))
	for _, n := range ns {
		out = append(out, envelope.ContainerNetwork{
			Name:        n.Name,
			IPAddress:   n.IPAddress,
			IPv6Address: n.IPv6Address,
		})
	}
	return out
}

// toPublishedPorts 只挑出**真正发布到宿主机**的端口。
//
// 容器里 EXPOSE 但没发布到主机的端口没有宿主机端口，
// 它们不属于"端口映射"，放在这里会让用户以为那个端口能从外面访问。
func toPublishedPorts(ports []runtime.Port) []envelope.PublishedPort {
	out := make([]envelope.PublishedPort, 0, len(ports))
	for _, p := range ports {
		if p.Public <= 0 || p.Public > 65535 {
			continue
		}
		if p.Private <= 0 || p.Private > 65535 {
			continue
		}
		proto := p.Type
		if proto == "" {
			proto = "tcp"
		}
		out = append(out, envelope.PublishedPort{
			HostIP:        p.IP,
			HostPort:      uint16(p.Public),
			ContainerPort: uint16(p.Private),
			Protocol:      proto,
		})
	}
	return out
}

// shortID 截取短 ID；长度不足时原样返回（异常数据不 panic）。
func shortID(id string) string {
	if len(id) <= shortIDLen {
		return id
	}
	return id[:shortIDLen]
}

// formatPorts 把端口映射拼成一行摘要，形如 "0.0.0.0:8080->80/tcp, 443/tcp"。
//
// 与 docker ps 的 PORTS 列保持同样的读法，降低运维的认知切换成本。
func formatPorts(ports []runtime.Port) string {
	if len(ports) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		proto := p.Type
		if proto == "" {
			proto = "tcp"
		}
		if p.Public > 0 {
			host := p.IP
			if host == "" {
				host = "0.0.0.0"
			}
			parts = append(parts, fmt.Sprintf("%s:%d->%d/%s", host, p.Public, p.Private, proto))
			continue
		}
		// 未映射到宿主机时只显示容器内端口（docker ps 同款写法）。
		parts = append(parts, fmt.Sprintf("%d/%s", p.Private, proto))
	}
	return strings.Join(parts, ", ")
}

// opPayload 解码启停类请求载荷。
func opPayload(payload []byte) (envelope.ContainerOpPayload, error) {
	var p envelope.ContainerOpPayload
	if len(payload) == 0 || string(payload) == "null" {
		return p, badRequestf("缺少载荷：需要 container_id")
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return p, badRequestf("载荷格式错误: %v", err)
	}
	if strings.TrimSpace(p.ContainerID) == "" {
		return p, badRequestf("container_id 不能为空")
	}
	return p, nil
}

// newStartHandler 处理 container.start。
func newStartHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := opPayload(payload)
		if err != nil {
			return nil, err
		}
		if err := rt.Start(ctx, p.ContainerID); err != nil {
			return nil, err
		}
		return opResult(ctx, rt, p.ContainerID), nil
	}
}

// newStopHandler 处理 container.stop。
//
// force 透传给运行时：force=true 走 kill，否则给容器优雅退出窗口。
func newStopHandler(rt runtime.Runtime) Handler {
	return func(ctx context.Context, payload []byte) (any, error) {
		p, err := opPayload(payload)
		if err != nil {
			return nil, err
		}
		if err := rt.Stop(ctx, p.ContainerID, p.Force); err != nil {
			return nil, err
		}
		return opResult(ctx, rt, p.ContainerID), nil
	}
}

// opResult 在操作成功后回读一次最新状态。
//
// 为什么要回读而不是直接回 "running"/"exited"：启动/停止是异步的，
// 引擎返回 204 只代表"命令已接受"。回读列表能拿到真实状态，前端不必再发一次
// 列表请求（协议注释里明确规定该行为）。回读失败不视为操作失败——操作本身已经成功，
// 只是拿不到最新状态，此时返回空状态即可。
func opResult(ctx context.Context, rt runtime.Runtime, idOrName string) envelope.ContainerOpResult {
	res := envelope.ContainerOpResult{ContainerID: idOrName}
	// 给状态刷新留一点点时间，避免刚好撞在引擎状态切换的瞬间。
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	containers, err := rt.List(readCtx, true)
	if err != nil {
		return res
	}
	for _, c := range containers {
		if matchRef(c, idOrName) {
			res.ContainerID = shortID(c.ID)
			res.State = c.State
			return res
		}
	}
	return res
}

// matchRef 判断容器是否匹配用户给的引用（完整 ID / 短 ID / 名称）。
func matchRef(c runtime.Container, ref string) bool {
	if ref == "" {
		return false
	}
	return c.ID == ref || c.Name == ref || (len(c.ID) >= len(ref) && strings.HasPrefix(c.ID, ref))
}
