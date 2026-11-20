package link

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"

	"arkade/protocol/envelope"
)

func (d *dialer) discover(ctx context.Context) (string, error) {
	endpoint := *d.origin
	endpoint.Path = envelope.AgentDiscoveryPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", fmt.Errorf("构造 Hub 接入发现请求失败: %w", err)
	}
	req.Header = d.headers()
	resp, err := d.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求 Hub 接入发现失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Hub 接入发现失败（HTTP %d）", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return "", fmt.Errorf("Hub 接入发现未返回 JSON，请检查反向代理路由")
	}

	const maxBody = 2048
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	var result envelope.AgentDiscovery
	if err != nil || len(body) > maxBody || json.Unmarshal(body, &result) != nil || !envelope.ValidAgentConnectPath(result.ConnectPath) {
		return "", fmt.Errorf("Hub 返回了无效的 Agent 接入路径")
	}
	return result.ConnectPath, nil
}
