package agentlink

// identity.go 负责 Agent 身份的生成与比较。
//
// 身份策略：重连时优先复用 Agent 自己记住的 ID（由 Hub 首次分配并让 Agent 持久化），
// 只有全新 Agent 才由 Hub 生成；生成规则基于主机名+架构做稳定哈希，
// 让同一台机器重装 Agent 后仍能得到可读、稳定的 ID。

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"

	"arkade/protocol/envelope"
)

// buildAgentID 为全新 Agent 生成 ID。
//
// 格式：<主机名前缀>-<8位哈希>。带可读前缀是为了排障时一眼看出是哪台机器；
// 后缀哈希用于区分同名主机（例如多个容器的 hostname 都叫 localhost）。
func buildAgentID(hello envelope.HelloPayload) string {
	host := strings.TrimSpace(hello.Hostname)
	if host == "" {
		// 没有主机名时退化为纯哈希 ID，仍保证唯一。
		host = "unknown"
	}
	sum := sha256.Sum256([]byte(host + "|" + hello.OS + "|" + hello.Arch))
	suffix := hex.EncodeToString(sum[:])[:8]

	prefix := sanitizeIDPart(host)
	if len(prefix) > 32 {
		prefix = prefix[:32]
	}
	return fmt.Sprintf("%s-%s", prefix, suffix)
}

// sanitizeIDPart 把主机名规范成可放入 URL 与文件名安全使用的字符集。
func sanitizeIDPart(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "agent"
	}
	return out
}

// subtleCompare 恒定时间字符串比较，防止通过响应时间逐字节猜测 Token。
// 返回 1 表示相等，0 表示不等。
func subtleCompare(a, b string) int {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b))
}
