package user

// identity.go 是用户与用户组标识的规整与校验，全项目唯一入口。
//
// 为什么必须集中：用户名同时出现在登录、创建用户、初始化管理员等多个入口。
// 如果每个入口各写一套 trim/小写规则，就会出现「注册时大写、登录时小写对不上」
// 或者「同名用户能建两个」这类问题。store 层明确要求「调用方必须已完成用户名规整」，
// 指的就是本文件的 NormalizeUsername。

import (
	"fmt"
	"strings"
	"unicode"
)

// 标识的长度与字符集约束。
const (
	// minUsernameLen 是最小长度。1 位用户名没有任何安全价值且极易撞名。
	minUsernameLen = 3
	// maxUsernameLen 是最大长度，避免界面与日志出现超长串。
	maxUsernameLen = 64
	// maxGroupNameLen 是用户组名上限。组名比用户名宽松（允许中文），但同样要限长。
	maxGroupNameLen = 64
	// maxDisplayNameLen 是显示名上限。
	maxDisplayNameLen = 64
	// maxDescriptionLen 是组描述上限。
	maxDescriptionLen = 255
)

// NormalizeUsername 规整用户名：去首尾空白并转小写。
//
// 转小写的理由：用户名是登录凭据，大小写敏感会带来「我明明输对了却登不上」的
// 支持成本，而小写化在小规模内网系统里不会损失可用性。
// 若未来要做大小写敏感账号，必须同时改这里与 users 表的唯一约束（需写字节级比较），
// 不能只在某一处改。
func NormalizeUsername(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ValidateUsername 校验规整后的用户名。
//
// 字符集限制为「字母、数字、下划线、连字符、点」：
//   - 禁止空格与斜杠，避免出现在 URL 路径或日志里产生歧义；
//   - 禁止 @、: 等字符，避免与未来的邮箱登录或 agent:xxx 这类目标格式混淆。
func ValidateUsername(s string) error {
	if len(s) < minUsernameLen {
		return fmt.Errorf("用户名至少 %d 个字符", minUsernameLen)
	}
	if len(s) > maxUsernameLen {
		return fmt.Errorf("用户名不能超过 %d 个字符", maxUsernameLen)
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		switch r {
		case '_', '-', '.':
			continue
		}
		return fmt.Errorf("用户名只能包含字母、数字、下划线、连字符与点（非法字符 %q）", r)
	}
	// 全是数字的用户名容易被误认为 ID，且在某些系统里与「用户 ID」混淆。
	if strings.Trim(s, "0123456789") == "" {
		return fmt.Errorf("用户名不能全为数字")
	}
	return nil
}

// ValidateDisplayName 校验显示名。
//
// 显示名允许任意可见字符（含中文），只要非空且不超长：它是给人看的，
// 过度限制字符集只会逼用户用拼音。
func ValidateDisplayName(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("显示名不能为空")
	}
	if len(s) > maxDisplayNameLen {
		return fmt.Errorf("显示名不能超过 %d 个字符", maxDisplayNameLen)
	}
	return nil
}

// NormalizeGroupName 规整用户组名：只去首尾空白，不做大小写转换。
//
// 与用户名不同的取舍：组名是展示与管理单位，中文与大小写差异（如 DEV / dev）
// 在运维语义上常常是刻意区分的两个组，强行小写会制造误合并。
func NormalizeGroupName(raw string) string {
	return strings.TrimSpace(raw)
}

// ValidateGroupName 校验组名。
func ValidateGroupName(s string) error {
	if s == "" {
		return fmt.Errorf("用户组名不能为空")
	}
	if len(s) > maxGroupNameLen {
		return fmt.Errorf("用户组名不能超过 %d 个字符", maxGroupNameLen)
	}
	return nil
}

// ValidateDescription 校验组描述（允许为空）。
func ValidateDescription(s string) error {
	if len(s) > maxDescriptionLen {
		return fmt.Errorf("描述不能超过 %d 个字符", maxDescriptionLen)
	}
	return nil
}

// ValidatePassword 是对外暴露的密码策略校验。
//
// 真正的策略在 password.go 的 validatePassword 里（HashPassword 会调用它），
// 这里只是把它转成可在「创建用户前预检」的形式：先校验再哈希，能把参数错误
// 与内部错误区分开，让上层返回 400 而不是 500。
func ValidatePassword(plain string) error {
	return validatePassword(plain)
}

// ValidRole 判断角色字符串是否合法（区分「不认识的脏值」与「未提供」）。
//
// ParseRole 会把未知值降级为普通用户，这是给读取数据库用的安全默认；
// 但处理用户输入时必须报错——把 admin 误写成 Admin 却静默降级成普通用户，
// 会让人以为授权成功了。
func ValidRole(s string) (Role, error) {
	switch Role(s) {
	case RoleAdmin:
		return RoleAdmin, nil
	case RoleUser:
		return RoleUser, nil
	default:
		return "", fmt.Errorf("角色只能是 %q 或 %q", RoleAdmin, RoleUser)
	}
}
