package user

// model.go 是用户与用户组的数据模型。
//
// 字段与 store 的建表语句一一对应；JSON 标签直接决定前端拿到的字段名，
// 改动任何标签都等价于改前端契约，需要同步通知 web 侧。

import "time"

// User 是登录用户。
type User struct {
	ID          string    `json:"id"`
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        Role      `json:"role"`
	Disabled    bool      `json:"disabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// PasswordHash 只在服务端内部流转，绝不参与 JSON 序列化。
	PasswordHash string `json:"-"`
}

// Group 是用户组。组绑定可访问的服务器范围。
type Group struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// GroupGrant 是「组 → 服务器 → 权限档位」的授权记录。
type GroupGrant struct {
	GroupID    string     `json:"group_id"`
	AgentID    string     `json:"agent_id"`
	Permission Permission `json:"permission"`
}

// 会话相关常量，store 与 auth 服务共用。
const (
	// sessionTokenBytes 是会话令牌的随机字节数。32 字节 = 256 位，
	// 爆破不可行。
	sessionTokenBytes = 32
)
