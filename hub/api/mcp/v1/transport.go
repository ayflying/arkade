package v1

import "github.com/gogf/gf/v2/frame/g"

// TransportReq delegates the raw JSON-RPC stream to the MCP SDK.
type TransportReq struct {
	g.Meta `path:"/mcp" method:"post,get,delete" tags:"MCP" raw:"true"`
}

type TransportRes struct{}
