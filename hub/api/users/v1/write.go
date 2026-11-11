package v1

import "github.com/gogf/gf/v2/frame/g"

// Validation stays in the controller to preserve the existing error messages.
type CreateReq struct {
	g.Meta      `path:"/api/users" method:"post" tags:"Users" summary:"创建用户"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
	Role        string `json:"role"`
}

type CreateRes struct {
	User UserView `json:"user"`
}

type UpdateReq struct {
	g.Meta      `path:"/api/users/{userID}" method:"patch" tags:"Users" summary:"修改用户"`
	UserID      string           `json:"-" in:"path" name:"userID"`
	DisplayName Optional[string] `json:"display_name"`
	Role        Optional[string] `json:"role"`
	Disabled    Optional[bool]   `json:"disabled"`
}

type UpdateRes struct {
	User UserView `json:"user"`
}

type ResetPasswordReq struct {
	g.Meta   `path:"/api/users/{userID}/password" method:"put" tags:"Users" summary:"重置密码"`
	UserID   string `json:"-" in:"path" name:"userID"`
	Password string `json:"password"`
}

// ResetPasswordRes is documentary: success returns controller.ErrNoContent.
type ResetPasswordRes struct{}

type DeleteReq struct {
	g.Meta `path:"/api/users/{userID}" method:"delete" tags:"Users" summary:"删除用户"`
	UserID string `json:"-" in:"path" name:"userID"`
}

// DeleteRes is documentary: success returns controller.ErrNoContent.
type DeleteRes struct{}
