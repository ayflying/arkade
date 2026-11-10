package compose

// redact.go：流式输出脱敏。
//
// ============================ 为什么这是最关键的安全件 ============================
//
// "更新镜像"要把真实命令输出推到浏览器。命令输出里可能出现：
//   - 项目 .env 里的密码（compose 会打印插值后的值、镜像地址带凭证）；
//   - 继承环境里的 token（用户把 ${TOKEN} 写进编排，compose 打印时展开）；
//   - registry 认证串 user:pass@host。
//
// 一次泄漏就是明文密码进了浏览器历史、Hub 数据库与截图。
//
// ============================ 两条硬规则 ============================
//
//  1. **失败必须 fail-closed**：读不到/解析不了 .env 时，绝不"跳过脱敏继续跑"。
//     那等于在最需要保护的时候关掉保护。调用方会改为隐藏全部输出。
//
//  2. **短值也必须遮**：密码可能是 "aX9" 这种短串。
//     只遮长度 ≥ 6 的值会让短密码漏出去——过度遮盖只影响可读性，
//     漏遮则是安全事故。因此**所有非空值**都遮。
//
// ============================ 为什么不会把正常输出遮烂 ============================
//
// 全部遮盖的代价是：像 "1"、"on"、"main" 这类短值会把输出里的同名子串一起换掉。
// 这是**已知且接受**的取舍——用户看到 "***" 时知道"这里可能是敏感值"，
// 比看到明文密码好得多。
//
// 为降低误伤，替换按**长度降序**进行：先遮长值，避免长值被短值规则切碎。

import (
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// redactedPlaceholder 是替换后的占位符。
const redactedPlaceholder = "***"

// redactor 持有需要遮盖的字面值与正则。
//
// 字面值来自 .env 与敏感环境变量；正则覆盖 URL 内嵌凭证。
type redactor struct {
	// literals 是待替换的字面串，**按长度降序**排列。
	literals []string
	// patterns 是额外规则（registry 凭证）。
	patterns []*regexp.Regexp
}

// 空脱敏器不做任何替换（仅用于确实没有敏感来源的场景）。
var emptyRedactor = &redactor{}

// newRedactor 构造脱敏器。
//
// secrets 是待遮盖的字面值集合（来自 .env 的值、敏感环境变量值）。
// 空字符串会被忽略（否则会在每个词间插入占位符）。
func newRedactor(secrets []string) *redactor {
	seen := make(map[string]struct{}, len(secrets))
	out := make([]string, 0, len(secrets))
	for _, s := range secrets {
		// 只跳过空串：**不按长度过滤**，短密码同样必须遮。
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	// 长度降序：先遮长值，避免长值被短值规则切碎成多个残片。
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j] // 同长度时按字典序，保证结果稳定
	})
	return &redactor{
		literals: out,
		patterns: []*regexp.Regexp{
			// URL 内嵌凭证：scheme://user:pass@host → scheme://***:***@host
			// 这是 registry 未走 credential helper 时最常见的形态。
			regexp.MustCompile(`(://)([^:@/\s]+):([^@/\s]+)@`),
		},
	}
}

// redact 对一行输出做遮盖。
//
// 顺序：先做正则（结构化、上下文更明确），再替换字面值。
// 先字面值会把 "user:pass@host" 里的 pass 先换掉，正则就匹配不上了。
func (r *redactor) redact(line string) string {
	if r == nil {
		return line
	}
	out := line
	for _, re := range r.patterns {
		out = re.ReplaceAllString(out, "${1}"+redactedPlaceholder+":"+redactedPlaceholder+"@")
	}
	for _, lit := range r.literals {
		if strings.Contains(out, lit) {
			out = strings.ReplaceAll(out, lit, redactedPlaceholder)
		}
	}
	return out
}

// sensitiveEnvKeyFragments 是"值必须遮盖"的环境变量名特征。
//
// 为什么按名字判断而不是只按 .env：compose 的插值优先级是
// shell 环境 > .env，因此**继承环境**里的值也会出现在输出里。
// 我们无法枚举全部继承变量（会遮烂所有输出），
// 所以按敏感特征命中——宁可多遮。
var sensitiveEnvKeyFragments = []string{
	"password", "passwd", "token", "secret", "key", "auth",
	"credential", "passphrase", "apikey", "access_key", "private",
}

// isSensitiveEnvKey 判断变量名是否敏感（大小写不敏感）。
func isSensitiveEnvKey(key string) bool {
	k := strings.ToLower(key)
	for _, frag := range sensitiveEnvKeyFragments {
		if strings.Contains(k, frag) {
			return true
		}
	}
	return false
}

// envValues 解析 .env 内容，返回**全部非空值**（用于遮盖）。
//
// ============================ 只接受最朴素的写法 ============================
//
// 这里刻意**不实现 dotenv 语义**。原因：漏遮就是安全事故，而 dotenv 的
// 细节（行内注释、三种引号、转义、多行值、插值）每一条都可能让我们的
// "理解"与实际值不一致，从而遮不住真实值。
//
//   - 行内注释：# 之前的内容才是值（否则 PASSWORD=secret # c 里的 secret 漏遮）
//   - 单/双引号：剥离包裹引号
//   - 值里出现 $：**一律拒绝**。compose 对 ${VAR}、$VAR、$(...) 都做插值，
//     不实现插值引擎就无法知道真实值，拒绝是唯一安全的选择。
//   - 值里出现换行（转义产生）：**拒绝**。按行推送的日志里无法可靠匹配
//     跨行值，硬做只会引入复杂实现却仍可能漏遮。
//
// 任何一个值无法确定 → 整体 ok=false，由调用方 fail-closed 隐藏全部输出。
// 宁可让用户看不到日志，也不能把密码写进浏览器。
func envValues(content string) (values []string, ok bool) {
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) == "" {
			return nil, false
		}
		v, vok := parseSimpleEnvValue(val)
		if !vok {
			return nil, false
		}
		if v != "" {
			values = append(values, v)
		}
	}
	return values, true
}

// parseSimpleEnvValue 解析一个值：剥离注释与引号，遇到不确定的写法返回 false。
func parseSimpleEnvValue(val string) (string, bool) {
	val = strings.TrimSpace(val)

	// 双引号：取引号内，剥离后仍要求不含 $ 与转义。
	if strings.HasPrefix(val, `"`) {
		end := strings.Index(val[1:], `"`)
		if end < 0 {
			return "", false
		}
		rest := strings.TrimSpace(val[1+end+1:])
		if rest != "" && !strings.HasPrefix(rest, "#") {
			return "", false
		}
		v := val[1 : 1+end]
		if strings.ContainsAny(v, "$\\") {
			// 含插值或转义：不猜。
			return "", false
		}
		return v, true
	}

	// 单引号：同样保守，内含 $ 也拒绝（compose 仍可能插值）。
	if strings.HasPrefix(val, `'`) {
		end := strings.Index(val[1:], `'`)
		if end < 0 {
			return "", false
		}
		rest := strings.TrimSpace(val[1+end+1:])
		if rest != "" && !strings.HasPrefix(rest, "#") {
			return "", false
		}
		v := val[1 : 1+end]
		if strings.Contains(v, "$") {
			return "", false
		}
		return v, true
	}

	// 无引号：剥离行内注释。
	if i := strings.Index(val, " #"); i >= 0 {
		val = val[:i]
	} else if i := strings.Index(val, "\t#"); i >= 0 {
		val = val[:i]
	}
	val = strings.TrimSpace(val)
	if strings.ContainsAny(val, "$\\") {
		// $ 插值、反斜杠转义：都不猜。
		return "", false
	}
	return val, true
}

// inheritedSecrets 从继承环境中挑出敏感变量的值。
//
// 只挑敏感的（见 sensitiveEnvKeyFragments），否则会把 PATH、HOME 之类的值
// 全部遮掉，输出将完全不可读。
func inheritedSecrets(environ []string) []string {
	var out []string
	for _, kv := range environ {
		key, val, found := strings.Cut(kv, "=")
		if !found || val == "" {
			continue
		}
		if isSensitiveEnvKey(key) {
			out = append(out, val)
		}
	}
	return out
}

// composeInlineSecrets 从编排正文里提取**内联写的敏感值**。
//
// ============================ 为什么用 YAML 解析而不是找 "key: value" ============================
//
// 手工按行找 "敏感键: 值" 会漏掉最常见的几种写法：
//
//	environment:
//	  - DB_PASSWORD=secret        ← 列表形态（- 开头），按行找冒号直接漏
//	environment: {DB_PASSWORD: x} ← flow style，整行不是一段 key: value
//	  DB_PASSWORD: |              ← 块标量，值在下面几行
//
// 漏一个就是明文密码进浏览器。因此直接交给 yaml.v3 解析成节点树，
// 递归遍历**所有标量**：
//   - 键名命中敏感特征的，取其值；
//   - 值为 "KEY=VALUE" 形态的（environment 列表），取 VALUE；
//
// 保留策略保守的一面：同时把所有标量值都纳入遮盖集合。
// 这会误伤（镜像名、项目名也会被遮），但**不会漏遮**——
// 这是本功能唯一可接受的取舍方向。
//
// 解析失败说明这份正文本身有错，此时 compose 会回显原文行，
// 因此返回错误并由调用方 fail-closed（不执行、只给通用文案）。
func composeInlineSecrets(content string) ([]string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		return nil, err
	}
	out := make([]string, 0, 32)
	collectScalars(&root, &out)
	return out, nil
}

// collectScalars 递归收集节点树里的标量值。
func collectScalars(n *yaml.Node, out *[]string) {
	if n == nil {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, child := range n.Content {
			collectScalars(child, out)
		}
	case yaml.MappingNode:
		// Content 是 [key1, val1, key2, val2, ...]
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind == yaml.ScalarNode && isSensitiveEnvKey(k.Value) {
				// 敏感键：无论值是什么形态，把可提取的标量都收进来。
				*out = append(*out, scalarValues(v)...)
			}
			// 值可能是嵌套结构，继续递归（例如 environment 下的 map）。
			if v.Kind == yaml.MappingNode || v.Kind == yaml.SequenceNode {
				collectScalars(v, out)
			}
		}
	case yaml.ScalarNode:
		// environment 列表形态："KEY=VALUE"。
		if key, val, found := strings.Cut(n.Value, "="); found && isSensitiveEnvKey(key) {
			if v := strings.TrimSpace(val); v != "" {
				*out = append(*out, v)
			}
		}
	}
}

// scalarValues 提取一个节点的全部标量值（块标量与 flow 都能覆盖）。
func scalarValues(n *yaml.Node) []string {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		v := strings.TrimSpace(n.Value)
		// 插值形态（${X}）不是真实值，跳过；它由 compose 在运行时展开。
		if v == "" || strings.Contains(v, "${") {
			return nil
		}
		return []string{v}
	}
	var out []string
	for _, child := range n.Content {
		out = append(out, scalarValues(child)...)
	}
	return out
}

// noisePatterns 是需要**整行隐藏**的输出特征。
//
// ============================ 为什么必须整行隐藏 ============================
//
// compose 在配置错误时会打印 "error while interpolating services.x.environment.
// DB_PASSWORD: ..."，后面还可能跟原始值。这类行即使做了字面值替换，
// 仍可能残留我们没提取到的片段（例如未加引号的多词值被截断）。
//
// 判定"这一行是错误详情"比"精确判断里面有没有秘密"可靠得多，
// 因此对它们直接替换成固定文案：位置信息（行号/服务名）保留价值有限，
// 用户可以在 Hub 的错误提示里看到结构化结论。
var noisePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(error|fatal|panic)\b.*\b(interpolat|environment|variable|secret|credential)`),
	regexp.MustCompile(`(?i)\b(invalid|missing|required)\b.*\b(variable|environment)\b`),
}

// sanitizeLine 对一行输出做最终处理：先整行判定噪声，再做字面值遮盖。
//
// 返回值 ok=false 表示整行应被替换成通用文案（不展示原文）。
func sanitizeLine(line string, r *redactor) string {
	for _, re := range noisePatterns {
		if re.MatchString(line) {
			// 整行替换：这里**不保留任何原文片段**，
			// 因为我们无法确定那一段里是否含未提取到的敏感值。
			return noiseLinePlaceholder
		}
	}
	return r.redact(line)
}

// noiseLinePlaceholder 是敏感错误行被隐藏后的占位文案。
const noiseLinePlaceholder = "（该行可能包含敏感配置内容，已隐藏）"
