package agents

import (
	"net/netip"
	"strings"

	"github.com/gogf/gf/v2/errors/gerror"
)

// NormalizeDirectAddress accepts only browser-usable IP addresses or hostnames,
// never URLs, credentials, ports, paths, or scoped IP addresses. The Hub must not
// use this metadata to establish server-side connections.
func NormalizeDirectAddress(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	invalid := gerror.New("直连地址请填写IP或域名，不含协议、端口和路径")
	if len(s) > 253 {
		return "", invalid
	}
	ipText := s
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		ipText = s[1 : len(s)-1]
	}
	if ip, err := netip.ParseAddr(ipText); err == nil {
		if ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
			return "", invalid
		}
		return ip.String(), nil
	}
	if strings.ContainsAny(s, "/:@?#[]\\") {
		return "", invalid
	}
	s = strings.ToLower(s)
	for _, part := range strings.Split(s, ".") {
		if len(part) == 0 || len(part) > 63 || part[0] == '-' || part[len(part)-1] == '-' {
			return "", invalid
		}
		for _, r := range part {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return "", invalid
			}
		}
	}
	return s, nil
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
