package agentlink

// credential.go 生成与校验「服务器专属接入密钥」。
//
// 设计要点：
//
//  1. 明文只在生成的那一瞬间存在于内存里，随后只输出给调用方一次。
//     Hub 数据库只存 SHA256，无法反推。
//  2. 摘要用 SHA256 而不是 bcrypt/argon2：这是**高熵随机密钥**
//     （32 字节随机），不是人类口令，不存在字典攻击的余地，
//     用慢哈希只会让每次握手都白白多花几十毫秒。
//  3. 与共享 token 的比对用常量时间，避免逐字节比较泄漏前缀信息。

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// EnrollTokenBytes 是专属密钥的随机字节数。
//
// 32 字节 = 256 位，base64url 后 43 个字符。
// 下限是任务要求的"至少 32 字节"，这里就取下限——
// 更长的密钥对安全性提升有限，却让人工抄写更容易出错。
const EnrollTokenBytes = 32

// NewEnrollmentToken 生成一把新的专属密钥，返回明文与其摘要。
//
// 返回值里明文只用一次（写进响应），摘要写库。
// 两者一起返回是为了让调用方无法"只拿到一个"——
// 分离成两个函数容易在重置流程里写错顺序，
// 出现"写了摘要但响应里给的是另一把密钥"这种致命错误。
func NewEnrollmentToken() (plain, hash string, err error) {
	b := make([]byte, EnrollTokenBytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand 失败意味着系统熵源有问题，
		// 此时绝不能退化到可预测的伪随机——直接失败。
		return "", "", fmt.Errorf("生成随机密钥失败: %w", err)
	}
	plain = base64.RawURLEncoding.EncodeToString(b)
	return plain, HashEnrollmentToken(plain), nil
}

// HashEnrollmentToken 计算密钥的摘要（十六进制 SHA256）。
//
// 入参是明文，出参是可直接存库的字符串。
// 单独暴露是为了让"按 token 查库"这条路径能复用同一份摘要算法——
// 两处各写一遍摘要逻辑，只要有一处改了（比如加了盐），
// 认证就会全部失败且难以定位。
func HashEnrollmentToken(plain string) string {
	sum := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(sum[:])
}

// ErrTokenAmbiguous 表示生成的密钥与共享 token 相同。
//
// 为什么要专门检查：认证时先按摘要查专属凭据、查不到再比对共享 token。
// 若一把密钥既是专属密钥又与共享 token 同值，认证路径就有了歧义——
// 同一个字符串可能被解释成两种身份，这正是提权漏洞的经典形态。
// 概率极低（2^-256），但检查成本几乎为零，且能排除一整类推理负担。
var ErrTokenAmbiguous = errors.New("生成的密钥与共享 token 相同")

// GenerateEnrollmentTokenAvoiding 生成一把与共享 token 不冲突的密钥。
//
// 用常量时间比对后重试。理论上第一次就不会撞，这里仍写成循环，
// 因为"概率极低"不是"不可能"，而静默接受一个歧义密钥是安全事故。
// 重试上限只是防止理论上的死循环，不是安全参数。
func GenerateEnrollmentTokenAvoiding(sharedToken string) (plain, hash string, err error) {
	const maxAttempts = 8
	for i := 0; i < maxAttempts; i++ {
		p, h, gerr := NewEnrollmentToken()
		if gerr != nil {
			return "", "", gerr
		}
		if sharedToken == "" || subtleCompare(p, sharedToken) != 1 {
			return p, h, nil
		}
	}
	return "", "", ErrTokenAmbiguous
}

// 常量时间比较复用 identity.go 里的 subtleCompare：
// 本包此前已经为共享 token 的比对实现了它，再写一份会在
// 将来加固时只改一处，留下不一致的比对面。
