package compose

// env.go 处理项目级 .env 文本的解析与基本校验。
//
// 只做"我们自己需要知道"的事，不试图实现一个完整的 dotenv 解析器：
// 真正的插值由 compose 自己做。这里唯一的目的，是拿到 .env 里定义了哪些键，
// 以便把这些键从 compose 子进程的继承环境里剔除（见 Runner.EnvExcludeKeys）。

import (
	"fmt"
	"strings"
)

// maxEnvContentBytes 是 .env 的大小上限。
//
// 它不是安全边界（真正的边界是 Agent 收到消息的总大小），
// 而是一个"明显不对劲"的提示：正常的项目级 .env 是几十行，
// 上兆的 .env 通常意味着用户粘错了文件（例如把备份包或二进制粘了进来）。
// 提早拒绝比让它落盘后再由 compose 报错更好排查。
const maxEnvContentBytes = 256 * 1024

// ValidateEnvContent 对 .env 文本做最低限度的合理性校验。
//
// 检查项刻意选"几乎不可能合法"的情形，避免误拒正常文件：
//   - 含 NUL 字节：文本文件不可能有，说明传了二进制；
//   - 超过大小上限；
//   - 存在没有任何 '=' 的非注释非空行：
//     正常 dotenv 每一行要么是注释/空行，要么是 KEY=VALUE。
//     一个孤立的单词行通常说明用户粘的是别的格式（比如 JSON 或 YAML）。
//     compose 对这种行会报错，提前发现能让错误信息更贴近用户的操作。
func ValidateEnvContent(content string) error {
	if content == "" {
		return nil
	}
	if len(content) > maxEnvContentBytes {
		return fmt.Errorf("环境变量内容过大（%d 字节，上限 %d）", len(content), maxEnvContentBytes)
	}
	if strings.ContainsRune(content, 0) {
		return fmt.Errorf("环境变量内容包含二进制数据（NUL 字节），请提供文本格式的 .env")
	}
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.Contains(trimmed, "=") {
			// **绝不回显行内容。**
			//
			// 一行"看起来不像 KEY=VALUE"的内容，很可能正是用户不小心粘贴进来的
			// 密钥、证书或某段密码文本。错误信息会进 Hub 返回体、界面和审计日志，
			// 一旦回显，明文密钥就被写进了三个地方的持久存储。
			// 行号足以让用户定位问题，内容他自己在编辑器里看得到。
			return fmt.Errorf("环境变量第 %d 行不是 KEY=VALUE 格式（仅支持 KEY=value、注释和空行）", i+1)
		}
	}
	return nil
}

// ParseEnvKeys 返回 .env 里定义的所有变量名。
//
// 比 ValidateEnvContent 宽松：解析不出的行直接跳过，不报错。
// 原因：这个函数的产物只用于"从继承环境里剔除同名变量"，
// 少剔除一个键的后果是"该键仍可能来自 Agent 进程环境"，
// 而为了一个格式怪异的行去中断整个操作，代价更大（用户会看不懂为什么失败）。
// 真正的格式问题由 ValidateEnvContent 在写入前挡下。
//
// 支持的写法（覆盖实际见到的绝大多数）：
//
//	KEY=value
//	export KEY=value      ← shell 风格的导出写法，dotenv 也接受
//	KEY="value" / KEY='value'
//	KEY=                  ← 空值，仍然算"定义了"
func ParseEnvKeys(content string) []string {
	if content == "" {
		return nil
	}
	seen := make(map[string]struct{})
	var keys []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// 去掉可选的 export 前缀。
		trimmed = strings.TrimPrefix(trimmed, "export ")
		trimmed = strings.TrimSpace(trimmed)

		key, _, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		// 变量名只允许字母数字下划线（dotenv 的实际约束）。
		// 含其它字符的行视为解析不了，跳过而不是猜测。
		if !isEnvKeyName(key) {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

// isEnvKeyName 判断是否像合法的环境变量名。
func isEnvKeyName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r == '_':
			continue
		case r >= '0' && r <= '9':
			// 数字不能作为首字符。
			if i == 0 {
				return false
			}
			continue
		default:
			return false
		}
	}
	return true
}
