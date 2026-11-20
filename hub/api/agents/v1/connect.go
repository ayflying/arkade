package v1

import "github.com/gogf/gf/v2/frame/g"

// ConnectReq is the reverse-channel transport, not a session-authenticated API.
type ConnectReq struct {
	g.Meta `path:"/agent/connect" method:"get" tags:"Transport" summary:"Agent 反向连接"`
}
type ConnectRes struct{}

// DiscoverReq returns the authenticated Agent's external WebSocket path.
type DiscoverReq struct {
	g.Meta `path:"/agent/discover" method:"get" tags:"Transport" summary:"发现 Agent 接入路径"`
}
type DiscoverRes struct {
	ConnectPath string `json:"connect_path"`
}
