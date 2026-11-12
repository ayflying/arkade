package v1

import "github.com/gogf/gf/v2/frame/g"

type UserView struct {
	ID                 string `json:"id"`
	Username           string `json:"username"`
	DisplayName        string `json:"display_name"`
	Role               string `json:"role"`
	MustChangePassword bool   `json:"must_change_password"`
	LocalPassword      bool   `json:"local_password"`
}
type GetReq struct {
	g.Meta `path:"/api/account" method:"get" tags:"Account" summary:"个人资料"`
}
type GetRes struct {
	User UserView `json:"user"`
}
type UpdateReq struct {
	g.Meta `path:"/api/account" method:"patch" tags:"Account" summary:"更新个人资料"`
}
type UpdateInput struct {
	DisplayName string `json:"display_name"`
}
type UpdateRes struct {
	User UserView `json:"user"`
}
type PasswordReq struct {
	g.Meta `path:"/api/account/password" method:"put" tags:"Account" summary:"修改本地密码"`
}
type PasswordInput struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password"`
}
type PasswordRes struct{}
type RegisterReq struct {
	g.Meta `path:"/api/auth/register" method:"post" tags:"Auth" summary:"注册本地账户"`
}
type RegisterInput struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Password    string `json:"password"`
}

// Register succeeds with 201, creates no session and requires explicit login.
type RegisterRes struct {
	User UserView `json:"user"`
}
