package v1

import "github.com/gogf/gf/v2/frame/g"

type ListReq struct {
	g.Meta `path:"/api/users" method:"get" tags:"Users" summary:"列出用户"`
}

type ListRes struct {
	Users []UserView `json:"users"`
}
