package v1

import (
	"time"

	"github.com/gogf/gf/v2/frame/g"
)

// KeyView contains only public metadata; token is returned solely on creation.
type KeyView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Prefix    string    `json:"prefix"`
	CreatedAt time.Time `json:"createdAt"`
}

type ListReq struct {
	g.Meta `path:"/api/mcp/keys" method:"get" tags:"MCP" summary:"列出自己的 MCP 密钥"`
}
type ListRes struct {
	Items []KeyView `json:"items"`
}

type CreateReq struct {
	g.Meta `path:"/api/mcp/keys" method:"post" tags:"MCP" summary:"创建 MCP 密钥"`
}
type CreateInput struct {
	Name string `json:"name"`
}
type CreateRes struct {
	Key   KeyView `json:"key"`
	Token string  `json:"token"`
}

type RevokeReq struct {
	g.Meta `path:"/api/mcp/keys/{keyID}" method:"delete" tags:"MCP" summary:"撤销 MCP 密钥"`
	KeyID  string `json:"-" in:"path" name:"keyID"`
}
type RevokeRes struct{}
