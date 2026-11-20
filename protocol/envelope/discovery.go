package envelope

import (
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// AgentDiscoveryPath is the fixed HTTP entry point on the configured Hub origin.
const AgentDiscoveryPath = "/agent/discover"

type AgentDiscovery struct {
	ConnectPath string `json:"connect_path"`
}

// ParseAgentHubOrigin accepts only an HTTP(S) origin, never URL credentials or
// paths. Its errors deliberately omit the input, which may contain credentials.
func ParseAgentHubOrigin(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" ||
		u.User != nil || strings.ContainsAny(raw, "?#") ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") {
		return nil, errors.New("Hub 地址必须为 http(s)://主机[:端口]，不能包含路径、凭据、查询参数或片段")
	}
	if port := u.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return nil, errors.New("Hub 地址端口必须在 1 至 65535 之间")
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("Hub 地址端口不能为空")
	}
	u.Path = ""
	return u, nil
}

var agentPrefixPattern = regexp.MustCompile(`^(/[A-Za-z0-9_-][A-Za-z0-9._~-]*)*$`)

// AgentConnectPathForPrefix joins the Hub's configured external proxy prefix
// with the fixed internal route. Reject ambiguous encodings, dot segments and
// authority-like paths rather than relying on proxy-specific normalization.
func AgentConnectPathForPrefix(prefix string) (string, error) {
	if !agentPrefixPattern.MatchString(prefix) || len(prefix) > 512 {
		return "", errors.New("Agent 网关前缀必须为空或形如 /arkade，不允许尾部斜杠")
	}
	return prefix + AgentConnectPath, nil
}

// ValidAgentConnectPath applies the same contract to a discovery response.
func ValidAgentConnectPath(path string) bool {
	prefix, ok := strings.CutSuffix(path, AgentConnectPath)
	if !ok {
		return false
	}
	_, err := AgentConnectPathForPrefix(prefix)
	return err == nil
}
