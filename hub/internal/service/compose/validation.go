package compose

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/gogf/gf/v2/errors/gerror"
	"strconv"
	"strings"
)

const MaxContentBytes = 1 << 20

func ConfigHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:16]
}
func ProjectHash(content, env string) string {
	if env == "" {
		return ConfigHash(content)
	}
	h := sha256.New()
	h.Write([]byte("arkade.compose.project.v1\x00content\x00"))
	h.Write([]byte(strconv.Itoa(len(content))))
	h.Write([]byte("\x00"))
	h.Write([]byte(content))
	h.Write([]byte("env\x00"))
	h.Write([]byte(strconv.Itoa(len(env))))
	h.Write([]byte("\x00"))
	h.Write([]byte(env))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
func ValidName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			continue
		}
		if (r == '_' || r == '-') && i > 0 {
			continue
		}
		return false
	}
	return true
}
func ValidateName(name string) error {
	if !ValidName(name) {
		return gerror.Newf("编排项目名不合法：只能包含小写字母、数字、下划线、连字符，且必须以字母或数字开头（实际 %q）", name)
	}
	return nil
}
func ValidateContent(content string) error {
	if strings.TrimSpace(content) == "" {
		return gerror.New("编排内容不能为空")
	}
	if len(content) > MaxContentBytes {
		return gerror.Newf("编排内容过大（%d 字节，上限 %d 字节）", len(content), MaxContentBytes)
	}
	return nil
}
func ValidateEnv(content string) error {
	if len(content) > MaxContentBytes {
		return gerror.New("环境文件超过1MB限制")
	}
	if strings.ContainsRune(content, '\x00') {
		return gerror.New("环境文件包含非法空字符")
	}
	return nil
}
func TruncateOutput(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 4000 {
		return "…（输出过长，已截断前部）\n" + string(r[len(r)-4000:])
	}
	return s
}
func SafeAutoReason(reasons []string) string {
	if len(reasons) == 0 {
		return "项目暂不满足自动接管条件"
	}
	r := []rune(reasons[0])
	if len(r) > 300 {
		r = r[:300]
	}
	return string(r)
}
func ValidTaskKey(s string) bool {
	if len(s) < 8 || len(s) > 128 {
		return false
	}
	return strings.IndexFunc(s, func(c rune) bool {
		return !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_')
	}) < 0
}
