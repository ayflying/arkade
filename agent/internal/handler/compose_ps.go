package handler

// compose_ps.go 把 compose ps 的输出转成协议里的容器摘要。
//
// 单独成文件的原因：这里要处理 compose ps 的三种输出形态，
// 与 compose.go 的业务流程混在一起会显得臃肿。
// 详细解析在 internal/compose/psjson.go，这里只做「运行时条目 → 协议结构」的映射。

import (
	"encoding/json"
	"fmt"
	"strings"

	"arkade/protocol/envelope"
)

// composePSEntry 是 compose ps --format json 的单个元素。
//
// 字段名与 Docker 的容器结构接近但不是同一个东西：
// compose 的 Name 是容器名，Service 是服务名，Publishers 是端口映射。
type composePSEntry struct {
	ID         string `json:"ID"`
	Name       string `json:"Name"`
	Service    string `json:"Service"`
	Image      string `json:"Image"`
	State      string `json:"State"`
	Status     string `json:"Status"`
	Health     string `json:"Health"`
	ExitCode   int    `json:"ExitCode"`
	Publishers []struct {
		URL           string `json:"URL"`
		TargetPort    int    `json:"TargetPort"`
		PublishedPort int    `json:"PublishedPort"`
		Protocol      string `json:"Protocol"`
	} `json:"Publishers"`
}

// composePSResult 把 compose ps 的原始 JSON 转成协议结果。
//
// 解析失败时返回空列表而不是错误：compose ps 的 JSON 在各版本里
// 有细微差异（有的版本每行一个对象，有的返回数组），
// 解析不出来时"没有容器"是比"整个请求失败"更可接受的降级——
// 用户至少还能通过日志和列表判断服务状态。
func composePSResult(raw string) envelope.ContainerListResult {
	entries := parseComposePS(raw)
	out := make([]envelope.ContainerSummary, 0, len(entries))
	for _, e := range entries {
		out = append(out, envelope.ContainerSummary{
			ID:      composeShortID(e.ID),
			Name:    e.Name,
			Image:   e.Image,
			State:   normalizeComposeState(e.State, e.Status),
			Status:  e.Status,
			Origin:  originArkade, // 来自编排的容器一定是 Arkade 创建的
			Created: 0,            // compose ps 不返回创建时间
			Ports:   composePorts(e),
		})
	}
	return envelope.ContainerListResult{Containers: out}
}

// parseComposePS 兼容两种输出形态。
func parseComposePS(raw string) []composePSEntry {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var entries []composePSEntry
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			return nil
		}
		return entries
	}
	var entries []composePSEntry
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var e composePSEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries
}

// composeShortID 截短容器 ID。
func composeShortID(id string) string {
	if len(id) <= shortIDLen {
		return id
	}
	return id[:shortIDLen]
}

// normalizeComposeState 归一化状态。
//
// compose ps 的 State 字段在部分版本里为空，只有 Status 文本
// （"Up 3 minutes"、"Exited (0) 2 minutes ago"），此时从文本前缀推断。
// 归一化的目的：让前端能用同一套状态图标渲染编排容器与普通容器。
func normalizeComposeState(state, status string) string {
	if s := strings.ToLower(strings.TrimSpace(state)); s != "" {
		switch s {
		case "running", "exited", "created", "restarting", "paused", "dead", "removing":
			return s
		default:
			return s
		}
	}
	s := strings.ToLower(strings.TrimSpace(status))
	switch {
	case strings.HasPrefix(s, "up"):
		return "running"
	case strings.HasPrefix(s, "exited"):
		return "exited"
	case strings.HasPrefix(s, "created"):
		return "created"
	case strings.HasPrefix(s, "restarting"):
		return "restarting"
	case strings.HasPrefix(s, "paused"):
		return "paused"
	case strings.HasPrefix(s, "dead"):
		return "dead"
	default:
		return ""
	}
}

// composePorts 把 Publishers 拼成与容器列表一致的端口摘要。
//
// 格式与 container.go 的 formatPorts 保持一致（"0.0.0.0:8080->80/tcp, 443/tcp"）：
// 同一份数据在前端不同页面长得一样，用户才不用在两种读法之间切换。
// 不直接复用 formatPorts 是因为入参类型不同（runtime.Port vs Publishers），
// 但输出格式刻意对齐。
func composePorts(e composePSEntry) string {
	if len(e.Publishers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(e.Publishers))
	for _, p := range e.Publishers {
		proto := p.Protocol
		if proto == "" {
			proto = "tcp"
		}
		if p.PublishedPort > 0 {
			host := p.URL
			if host == "" {
				host = "0.0.0.0"
			}
			parts = append(parts, fmt.Sprintf("%s:%d->%d/%s", host, p.PublishedPort, p.TargetPort, proto))
			continue
		}
		// 未映射到宿主机时只显示容器内端口（docker ps 同款写法）。
		parts = append(parts, fmt.Sprintf("%d/%s", p.TargetPort, proto))
	}
	return strings.Join(parts, ", ")
}
