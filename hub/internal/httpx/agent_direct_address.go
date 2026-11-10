package httpx

import (
	"fmt"
	"net/netip"
	"strings"
)

// 仅接受浏览器可使用的主机名，不接受URL、凭据、端口和路径。
// Hub不连接此地址，不能把这项元数据变成服务端代理或SSRF入口。
func normalizeDirectAddress(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", nil
	}
	invalid := fmt.Errorf("直连地址请填写IP或域名，不含协议、端口和路径")
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
