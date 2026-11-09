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
		ID:      shortID(c.ID),
		Name:    c.Name,
		Image:   c.Image,
		State:   c.State,
		Status:  c.Status,
		Origin:  originDiscovered,
		Created: c.CreatedUnix,
		Ports:   formatPorts(c.Ports),
	}
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
