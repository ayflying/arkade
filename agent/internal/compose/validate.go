package compose

// validate.go 是项目名与服名的白名单校验，属于安全边界代码。
//
// 为什么必须严格：项目名会被拼进两个危险位置——
//  1. 本机文件路径 <data_dir>/stacks/<name>/compose.yml
//  2. docker compose -p <name> 的命令行参数
// 一旦允许 "."、".."、"/"、" " 或 shell 元字符，就能越界写文件或注入命令。
// 逐字符白名单（只允许什么）比黑名单（禁止什么）可靠得多：黑名单永远漏。

import (
	"fmt"
	"strings"
)

const (
	// maxNameLen 与 Hub 侧一致，避免一边能存另一边拒收。
	maxNameLen = 64
	// maxServiceLen 是服务名长度上限，与 compose 自身的限制量级相当。
	maxServiceLen = 63
)

// ValidateName 校验编排项目名。
//
// 规则：^[a-z0-9][a-z0-9_-]*$，长度 1..64。
//
// 逐条说明为什么：
//   - 只允许小写字母：Docker Compose 的项目名本身要求小写，大写会被它自己拒绝，
//     与其让命令失败再翻译错误，不如提前给出明确文案；
//   - 首字符必须是字母或数字："." 在路径里是当前目录，"-" 打头的参数会被 docker
//     当成选项解析（经典参数注入），"_" 打头的目录在某些工具里被视为隐藏；
//   - 禁止点号：这是防 ".." 穿越最直接的手段——只要不允许点，就构造不出 ..；
//   - 禁止斜杠与反斜杠：路径分隔符，直接对应越界写文件；
//   - 禁止空格与 shell 元字符：虽然我们用 exec.Command 传参（不经过 shell），
//     但错误信息、日志、后续可能引入的 shell 调用都会放大这个风险，
//     在入口处掐死比事后每处都小心便宜得多。
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("编排项目名不能为空")
	}
	if len(name) > maxNameLen {
		return fmt.Errorf("编排项目名过长（%d 字节，上限 %d）", len(name), maxNameLen)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
			if i == 0 {
				return fmt.Errorf("编排项目名不能以 %q 开头，必须以小写字母或数字开头", string(c))
			}
		default:
			return fmt.Errorf("编排项目名含非法字符 %q：只允许小写字母、数字、下划线、连字符，且必须以字母或数字开头", string(c))
		}
	}
	return nil
}

// ValidateService 校验 compose 服务名。
//
// 服务名不如项目名危险（只作为命令的附加参数，不拼路径），但仍然会进入命令行，
// 因此用同一套白名单。额外允许点号：compose 里服务名带点是合法的，
// 且服务名不参与路径拼接，没有穿越风险。
func ValidateService(name string) error {
	if name == "" {
		return fmt.Errorf("服务名不能为空")
	}
	if len(name) > maxServiceLen {
		return fmt.Errorf("服务名过长（%d 字节，上限 %d）", len(name), maxServiceLen)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.':
			if i == 0 && c != '_' {
				// 首字符允许字母数字下划线；连字符与点开头显得可疑，直接拒。
				if c != '.' {
					return fmt.Errorf("服务名不能以 %q 开头", string(c))
				}
			}
		default:
			return fmt.Errorf("服务名含非法字符 %q", string(c))
		}
	}
	return nil
}

// ValidateServices 批量校验服务名列表，返回第一个错误。
func ValidateServices(names []string) error {
	for _, n := range names {
		if err := ValidateService(n); err != nil {
			return err
		}
	}
	return nil
}

// IsValidName 是 ValidateName 的布尔版本，用于列表过滤（跳过非法目录名而不是报错）。
func IsValidName(name string) bool { return ValidateName(name) == nil }

// CleanNameForSource 供 compose.source 使用：去空白 + 校验项目名。
//
// 导出版本而不是让调用方自己写一遍校验：项目名会进入容器标签匹配
// 与错误文案，校验规则必须只有一份。
func CleanNameForSource(name string) (string, error) { return cleanName(name) }

// cleanName 去掉首尾空白后校验，返回规范化後的名称。
// 项目名不做大小写转换：Compose 本身要求小写，转了小写反而会掩盖用户的输入错误。
func cleanName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if err := ValidateName(n); err != nil {
		return "", err
	}
	return n, nil
}
