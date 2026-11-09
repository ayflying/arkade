package compose

// services.go 从 compose 配置里解析服务名列表。
//
// 为什么不引入 YAML 库：
//   - 团队明确要求不新增依赖，而 compose 文件是用户可控输入，
//     一个完整的 YAML 解析器还会引入解析炸弹、别名展开等安全面；
//   - 我们只需要"services 下面有哪些键"这一条信息，不需要值、不需要类型；
//   - compose 配置里服务名的缩进位置是有强约定的（services: 下的第二层），
//     行解析足够，且失败时只是服务名不全（状态探测会回落到 ps 输出），不会崩。
//
// 解析边界（务必知道）：
//   - 只处理空格缩进的常规写法，不支持 tab 缩进（YAML 规范本身也禁止 tab 缩进）；
//   - 支持行内注释（# 之后）；# 在引号内的情形不处理（服务名不含 #，影响极小）；
//   - 若 services: 与第一个服务写在同一行（如 services: {web: ...}），
//     这种流式写法不做解析——极罕见，且解析失败只影响服务名展示。

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ParseServices 从 compose 文本里解析出服务名列表（升序、去重）。
//
// 解析算法：
//  1. 逐行扫描，找到顶格（缩进 0）的 "services:" 行，记下它的缩进作为基准；
//  2. 继续向下，遇到缩进更小的顶格行（如 "volumes:"）就结束；
//  3. 在 services 块内，取"比基准缩进深一层"且在下一层里最靠左的那批键名，
//     就是服务名。
func ParseServices(content string) []string {
	lines := strings.Split(content, "\n")

	seen := make(map[string]struct{})
	serviceIndent := -1
	inServices := false
	found := false

	for _, raw := range lines {
		line := stripComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := indentWidth(line)
		trimmed := strings.TrimSpace(line)

		if !inServices {
			// 找顶格的 services: 键。
			if indent == 0 && isKey(trimmed, "services") {
				inServices = true
				found = true
			}
			continue
		}

		// 已在 services 块内：遇到缩进回到 0 的其他顶格键，说明块结束。
		if indent == 0 {
			break
		}

		// 确定服务名的缩进层级：块内第一个有缩进的行决定它。
		if serviceIndent < 0 {
			serviceIndent = indent
		}
		// 只收恰好处在服务名层级的行，更深的是服务的属性（image、ports…）。
		if indent != serviceIndent {
			continue
		}
		name, ok := keyName(trimmed)
		if !ok {
			continue
		}
		if ValidateService(name) != nil {
			continue
		}
		seen[name] = struct{}{}
	}

	if !found {
		return nil
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// stripComment 去掉行内注释。
//
// 只按第一个 '#' 截断，不处理引号内的 '#'：服务名与常见配置里不会出现
// 引号内含 '#' 的情况，为它写状态机得不偿失。
func stripComment(line string) string {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		return line[:i]
	}
	return line
}

// indentWidth 计算行首空格数。tab 按 2 个空格计（虽然 YAML 禁用 tab 缩进，
// 但用户手写时仍可能混入，宽容处理比直接失败好）。
func indentWidth(line string) int {
	w := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			w++
		case '\t':
			w += 2
		default:
			return w
		}
	}
	return w
}

// isKey 判断该行是否是某个顶格键（形如 "services:"）。
func isKey(trimmed, key string) bool {
	return trimmed == key+":" || strings.HasPrefix(trimmed, key+": ")
}

// keyName 从 "web:" 或 "web: {}" 这类行里取出键名。
func keyName(trimmed string) (string, bool) {
	// 只认 "name:" 或 "name: value" 形式。
	idx := strings.IndexByte(trimmed, ':')
	if idx <= 0 {
		return "", false
	}
	name := strings.TrimSpace(trimmed[:idx])
	// 列表项 "- xxx" 不是键。
	if strings.HasPrefix(name, "-") {
		return "", false
	}
	// 键名不能含空格（含空格说明我们截错了位置）。
	if strings.ContainsAny(name, " \t\"'") {
		return "", false
	}
	return name, true
}

// servicesFromConfig 解析项目目录下的 compose 文件，取服务名。
// 文件读取失败时返回 nil（不当作错误：服务名只是展示信息，
// 真正的权威状态来自 docker compose ps）。
func servicesFromConfig(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return ParseServices(string(data))
}

// composePathIn 返回目录下的编排文件路径（供 status 使用，避免重复拼路径）。
func composePathIn(dir string) string {
	return filepath.Join(dir, composeFileName)
}
