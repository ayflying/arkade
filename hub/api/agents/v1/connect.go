package v1

import "github.com/gogf/gf/v2/frame/g"

// ConnectReq is the reverse-channel transport, not a session-authenticated API.
type ConnectReq struct {
	g.Meta `path:"/agent/connect" method:"get" tags:"Transport" summary:"Agent 反向连接"`
}
type ConnectRes struct{}
