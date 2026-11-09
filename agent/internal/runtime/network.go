package runtime

// network.go 实现网络相关端点。
//
// 网络删除最常见的失败原因是"还有容器接在这个网络上"（409）。
// 这里不做"先自动断开容器再删"的激进处理——那会静默改动用户的服务拓扑，
// 正确做法是把原因说清楚让用户先停容器。

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

// networkListResp 是 GET /networks 的响应元素。
type networkListResp struct {
	ID         string            `json:"Id"`
	Name       string            `json:"Name"`
	Driver     string            `json:"Driver"`
	Scope      string            `json:"Scope"`
	Internal   bool              `json:"Internal"`
	Labels     map[string]string `json:"Labels"`
	Created    string            `json:"Created"` // RFC3339 字符串，需转换
	Containers map[string]struct {
		Name string `json:"Name"`
	} `json:"Containers"`
}

// ListNetworks 列出网络。
func (e *engine) ListNetworks(ctx context.Context) ([]Network, error) {
	var raw []networkListResp
	if err := e.client.getJSON(ctx, pathNetworks, &raw); err != nil {
		return nil, e.translate(err)
	}
	out := make([]Network, 0, len(raw))
	for _, r := range raw {
		out = append(out, Network{
			ID:       r.ID,
			Name:     r.Name,
			Driver:   r.Driver,
			Scope:    r.Scope,
			Internal: r.Internal,
			// len(Containers) 就是接入容器数：Docker 把已接入容器列在 Containers 映射里。
			Containers: len(r.Containers),
			Labels:     r.Labels,
			Created:    parseRFC3339(r.Created),
		})
	}
	return out, nil
}

// RemoveNetwork 删除网络。
func (e *engine) RemoveNetwork(ctx context.Context, networkID string) error {
	ref := strings.TrimSpace(networkID)
	if ref == "" {
		return errors.New("网络 ID 不能为空")
	}
	if err := e.client.delete(ctx, pathNetworks+"/"+url.PathEscape(ref)); err != nil {
		return translateResourceError(err, "网络")
	}
	return nil
}
