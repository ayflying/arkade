package agents

import (
	"net/netip"
	"strconv"
	"strings"

	"github.com/gogf/gf/v2/errors/gerror"
)

// NormalizeDirectAddress validates the server's browser access address.
//
// 2026-11 起的语义（用户明确要求）：必须是带 http:// 或 https:// 前缀的完整地址，
// 例如 `https://ac8b255bfcf6-0.aynastv.5ddd.com` 或 `http://10.0.0.12:9000`。
// 协议与端口都由这个字段决定，容器端口链接不再自己猜 http，也不再自己拼端口。
//
// 为什么强制要求协议前缀，而不是「没写就默认 http」：
// 用户往往把域名反代（只挂 TLS）填进来，默认 http 会拼出一个必然打不开的地址；
// 而错链接比没有链接更难排查——用户会以为端口映射坏了，而不是配置少写了协议。
//
// 仍然拒绝（fail closed）：userinfo、路径、查询串、片段、非 http(s) 协议、
// 以及任何带 zone / 通配 / 组播地址。Hub 不拿这个字段发起服务端连接，
// 不能把它变成 SSRF 入口。
//
// 兼容：PATCH 时省略该字段不会触发校验，历史纯主机值原样保留在库里
// （界面上会提示去补全为完整地址）。
func NormalizeDirectAddress(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	invalid := gerror.New("访问地址请填写 http:// 或 https:// 开头的完整地址，例如 https://example.com，不含路径和查询参数")
	if len(s) > 253 {
		return "", invalid
	}

	scheme := ""
	rest := s
	for _, candidate := range []string{"https://", "http://"} {
		if len(s) >= len(candidate) && strings.EqualFold(s[:len(candidate)], candidate) {
			scheme = strings.ToLower(candidate)
			rest = s[len(candidate):]
			break
		}
	}
	// 没有协议前缀：不是完整地址，直接拒绝（不猜 http/https）
	if scheme == "" {
		return "", invalid
	}
	// 结尾斜杠容忍（从浏览器地址栏复制很常见）
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return "", invalid
	}

	host, portText, ok := splitHostPort(rest)
	if !ok {
		return "", invalid
	}
	// 带方括号的 IPv6 在拆分时已剥掉方括号，这里补回来（URL 里必须带）
	if strings.Contains(rest, "[") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	host = normalizeValidHost(host)
	if host == "" {
		return "", invalid
	}
	if portText == "" {
		return scheme + host, nil
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return "", invalid
	}
	return scheme + host + ":" + strconv.Itoa(port), nil
}

// splitHostPort 拆分 `host` / `host:port` / `[ipv6]` / `[ipv6]:port`，
// 裸 IPv6 视为「只有主机」。出现路径、查询、片段、userinfo 时返回 ok=false。
// 注意：带方括号时返回的是**剥掉方括号**的 IPv6 文本，由调用方补回。
func splitHostPort(rest string) (host, port string, ok bool) {
	if strings.ContainsAny(rest, "@?#\\/") {
		return "", "", false
	}
	if strings.HasPrefix(rest, "[") {
		close := strings.Index(rest, "]")
		if close < 0 {
			return "", "", false
		}
		host = rest[1:close]
		tail := rest[close+1:]
		if tail == "" {
			return host, "", true
		}
		if !strings.HasPrefix(tail, ":") {
			return "", "", false
		}
		if strings.Contains(tail[1:], ":") {
			return "", "", false
		}
		return host, tail[1:], true
	}
	switch strings.Count(rest, ":") {
	case 0:
		return rest, "", true
	case 1:
		idx := strings.LastIndex(rest, ":")
		if idx == 0 || idx == len(rest)-1 {
			return "", "", false
		}
		return rest[:idx], rest[idx+1:], true
	default:
		// 多个冒号：裸 IPv6，不允许再带端口
		return rest, "", true
	}
}

// normalizeValidHost 校验去掉协议之后的主机部分，非法返回 ""。
// 允许带方括号的 IPv6（`[2001:db8::1]`），规范化后仍保留方括号——
// 输出直接拼进 URL，裸 IPv6 会和多段冒号纠缠不清。
func normalizeValidHost(host string) string {
	if host == "" || len(host) > 253 {
		return ""
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		inner := host[1 : len(host)-1]
		if strings.Contains(inner, ":") {
			if ip, err := netip.ParseAddr(inner); err == nil {
				if ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
					return ""
				}
				return "[" + ip.String() + "]"
			}
		}
		return ""
	}
	if strings.ContainsAny(host, "/:@?#[]\\") {
		return ""
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
			return ""
		}
		// 裸 IPv6 补方括号，方便直接拼进 URL
		if strings.Contains(ip.String(), ":") {
			return "[" + ip.String() + "]"
		}
		return ip.String()
	}
	lower := strings.ToLower(host)
	for _, part := range strings.Split(lower, ".") {
		if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return ""
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return ""
			}
		}
	}
	return lower
}

// ValidateLabels preserves the admin patch validation messages and checks.
// An empty message means validation succeeded; labels are never normalized.
func ValidateLabels(labels map[string]string) string {
	if len(labels) > 32 {
		return "标签数量不能超过 32 个"
	}
	for k, v := range labels {
		if strings.TrimSpace(k) == "" {
			return "标签名不能为空"
		}
		if len([]rune(k)) > 64 {
			return "标签名不能超过 64 个字符"
		}
		if len([]rune(v)) > 256 {
			return "标签值不能超过 256 个字符"
		}
	}
	return ""
}

// ValidateCreate preserves enrollment validation order and messages. Only the
// name is trimmed before validation; the note's original rune count is checked.
func ValidateCreate(name, note string, labels map[string]string) (normalizedName string, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", gerror.New("服务器名称不能为空")
	}
	if len([]rune(name)) > 128 {
		return "", gerror.New("服务器名称过长（最多 128 个字符）")
	}
	if len([]rune(note)) > 2000 {
		return "", gerror.New("备注过长（最多 2000 个字符）")
	}
	if len(labels) > 32 {
		return "", gerror.New("标签数量过多（最多 32 个）")
	}
	for k, v := range labels {
		if strings.TrimSpace(k) == "" || len([]rune(k)) > 64 || len([]rune(v)) > 256 {
			return "", gerror.New("标签的键或值不合法")
		}
	}
	return name, nil
}
