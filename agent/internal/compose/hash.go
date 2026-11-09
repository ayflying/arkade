package compose

// hash.go 负责编排内容哈希。
//
// 与 Hub 侧 composeConfigHash 必须产生完全相同的结果：
// sha256(内容) 的十六进制前 16 位。两边算法不一致的后果是界面上
// 永远显示"配置已偏离"，真正的偏离反而被淹没。

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"os"
	"strconv"
)

// Hash 计算一段编排内容的哈希。
//
// 注意：直接对原始字节求哈希，不做换行归一化（不把 CRLF 转 LF）。
// 理由：Hub 存的是用户提交的原文，Agent 落盘的也是同一份原文，
// 两边都不该在做哈希时"顺手规整"——那样会让哈希不再等价于"文件内容"，
// 将来出现问题时无法用 sha256sum 交叉验证。
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:hashHexLen]
}

// HashFile 计算文件内容的哈希。文件不存在时返回错误。
func HashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return Hash(string(data)), nil
}

// HashProject 计算"编排项目"的哈希：compose.yml 与 .env 的联合哈希。
//
// ============================ 为什么需要它 ============================
//
// 只哈希 compose.yml 是不够的：同一个 compose.yml 配上不同的 .env，
// 起来的是完全不同的东西（镜像 tag、端口、密码都可能来自 .env）。
// 若哈希不含 .env，用户改了 .env 后界面仍显示"配置一致"，
// 而实际运行的是旧变量——这类"界面说没变、实际变了"的偏差最难排查。
//
// ============================ 为什么 env 为空时退化成 Hash ============================
//
// 兼容性要求：旧 Hub 只发 content、不发 env_content，
// 它算出的 content_hash 是 sha256(content)[:16]。
// 若空 env 也走联合算法，新旧两边会永远算出不同的值，
// 表现为**升级后所有编排都显示"配置已偏离"**——一次升级引发的全量误报。
//
// 因此明确规定：env 为空 ⟺ 与旧算法完全一致。
// 这条不是优化，是协议的一部分，Hub 侧必须实现同样的分支。
//
// ============================ 为什么带长度前缀 ============================
//
// 直接拼接会造成碰撞：content="a" + env="bc" 与 content="ab" + env="c"
// 拼出来是同一个字节串。长度前缀让每个字段的边界无歧义，
// 从而"不同内容 ⇒ 不同哈希"这条性质真正成立。
func HashProject(content, envContent string) string {
	if envContent == "" {
		// 旧版兼容路径：必须与 Hash 逐字节相同。
		return Hash(content)
	}

	h := sha256.New()
	// 域分隔前缀：把"项目哈希"与任何其它用途的 sha256 区分开，
	// 避免同一个字节串在别的场景里被复用成另一种含义。
	h.Write([]byte("arkade.compose.project.v1\x00content\x00"))
	writeSized(h, content)
	h.Write([]byte("env\x00"))
	writeSized(h, envContent)

	return hex.EncodeToString(h.Sum(nil))[:hashHexLen]
}

// writeSized 写入"长度 + 分隔符 + 内容"，让字段边界无歧义。
func writeSized(h hash.Hash, s string) {
	h.Write([]byte(strconv.Itoa(len(s))))
	h.Write([]byte("\x00"))
	h.Write([]byte(s))
}
