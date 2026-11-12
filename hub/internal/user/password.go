package user

import (
	"fmt"
	"strings"
)

// password.go 负责密码哈希。
//
// 选型：bcrypt（golang.org/x/crypto/bcrypt）。
//   - 自带随机盐，不需要额外维护 salt 字段；
//   - 计算成本可调（cost），默认值随库升级而提高；
//   - 故意设计为慢速，抵抗离线爆破。
//
// 边界：bcrypt 只处理前 72 字节，超长密码会被静默截断——
// 这里在入口处显式拒绝超长密码，避免"改最后几位密码却没变"的诡异现象。
const (
	// bcryptCost 是计算成本。默认 10 在 2025 年前后仍是安全与性能的平衡点，
	// 登录耗时约几十毫秒，对用户体验无感。
	bcryptCost = 10
	// maxPasswordBytes 是 bcrypt 的硬上限。
	maxPasswordBytes = 72
	// minPasswordLen 用于正常创建账号和修改密码，不限制部署初始密码。
	minPasswordLen = 8
)

// HashPassword 生成密码哈希。
func HashPassword(plain string) (string, error) {
	if err := validatePassword(plain); err != nil {
		return "", err
	}
	return hashPassword(plain)
}

// HashInitialPassword 仅用于部署时创建初始管理员，允许短密码；
// 保留非空白和 bcrypt 上限检查，不放宽正常创建账号/改密规则。
func HashInitialPassword(plain string) (string, error) {
	if strings.TrimSpace(plain) == "" {
		return "", fmt.Errorf("初始密码不能为空或全为空白字符")
	}
	if len(plain) > maxPasswordBytes {
		return "", fmt.Errorf("密码不能超过 %d 字节（bcrypt 上限）", maxPasswordBytes)
	}
	return hashPassword(plain)
}

func hashPassword(plain string) (string, error) {
	hash, err := bcryptGenerate([]byte(plain), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("生成密码哈希失败: %w", err)
	}
	return string(hash), nil
}

// VerifyPassword 校验密码。返回 nil 表示通过。
//
// 注意：这里故意不区分"用户不存在"与"密码错误"，由调用方统一处理，
// 避免账号枚举攻击。
func VerifyPassword(hashed, plain string) error {
	if err := bcryptCompare([]byte(hashed), []byte(plain)); err != nil {
		return fmt.Errorf("密码校验失败: %w", err)
	}
	return nil
}

// validatePassword 做长度与字符集的基础校验。
func validatePassword(plain string) error {
	if len(plain) < minPasswordLen {
		return fmt.Errorf("密码至少 %d 位", minPasswordLen)
	}
	if len(plain) > maxPasswordBytes {
		return fmt.Errorf("密码不能超过 %d 字节（bcrypt 上限）", maxPasswordBytes)
	}
	if strings.TrimSpace(plain) == "" {
		return fmt.Errorf("密码不能全为空白字符")
	}
	return nil
}
