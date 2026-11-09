// Package user 负责用户、用户组与权限判定（决策 D-04）。
//
// 职责边界：
//   - 本包只管「谁能做什么」的判定，不负责发 Cookie、不负责 HTTP 状态码；
//   - 密码哈希与校验也在这里，登录流程只调用 Verify；
//   - 权限按「组 × 服务器」授予取并集，三档 view < operate < manage。
package user

// Permission 是权限档位。数值越大权限越高，比较时直接用 >= 判断，
// 避免写一堆布尔组合。
type Permission int

const (
	// PermNone 表示没有任何访问权。
	PermNone Permission = iota
	// PermView 只看不改：能看服务器与容器列表。
	PermView
	// PermOperate 能启停容器等常规操作。
	PermOperate
	// PermManage 能删除容器、改 Agent、改凭据与用户组配置。
	PermManage
)

// String 返回可序列化的档位名，落库与传给前端的都是这个值。
func (p Permission) String() string {
	switch p {
	case PermView:
		return "view"
	case PermOperate:
		return "operate"
	case PermManage:
		return "manage"
	default:
		return "none"
	}
}

// ParsePermission 解析档位字符串，未知值返回 PermNone 而不是报错：
// 数据库里出现脏数据时应该降级为无权限（安全默认），而不是让服务崩掉。
func ParsePermission(s string) Permission {
	switch s {
	case "view":
		return PermView
	case "operate":
		return PermOperate
	case "manage":
		return PermManage
	default:
		return PermNone
	}
}

// Satisfies 判断当前档位是否满足所需档位。
func (p Permission) Satisfies(need Permission) bool { return p >= need }

// Role 是用户角色。首期为简化模型：admin 全局管理，user 受组授权限制。
type Role string

const (
	RoleAdmin Role = "admin" // 全局管理：不受服务器范围限制
	RoleUser  Role = "user"  // 普通用户：权限完全来自所属用户组
)

// ParseRole 解析角色字符串，未知值降级为普通用户（安全默认）。
func ParseRole(s string) Role {
	if Role(s) == RoleAdmin {
		return RoleAdmin
	}
	return RoleUser
}
