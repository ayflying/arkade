package compose

// import_scan.go 是"导入外部编排前的安全扫描"。
//
// ============================ 为什么需要扫描 ============================
//
// 导入会把编排从"宿主机上的某个目录"复制到管理区 <data_dir>/stacks/<name>/。
// 基准目录一变，所有**相对路径**的语义就变了：`env_file: ./app.env`
// 原本指向宿主机原目录，导入后指向管理区里不存在的路径。
// 后果不是干净地报错，而是 compose 静默用默认值启动，或起不来而用户不知为何。
// 宁可拒绝，也不能给一个"看起来成功、实际行为已经变了"的结果。
//
// ============================ 为什么用真正的 YAML 解析 ============================
//
// 早先版本用逐行字符串匹配，那是不够的：
//   - flow style：`services: {a: {env_file: ./x}}` 一行就能藏住一个危险字段；
//   - anchors/merge：`<<: *base` 会把另一个块里的 env_file/build 合并进来，
//     按行扫描根本看不到那个字段；
//   - 多文档（--- 分隔）、引号与转义、块标量（| >）都会让行匹配判断错位。
// 漏掉一个 env_file 就会让用户导入一份行为已变的编排，因此必须按语法结构判断。
//
// 用的是 gopkg.in/yaml.v3（项目已有依赖）的**节点树**而不是解码到 struct：
//   - 保留行号，能给出"第 N 行"的定位；
//   - 不需要为 compose 的全部字段定义结构体（漏定义就会漏检）；
//   - 未定义/扩展字段（x-*）也能被看到。
//
// ============================ 绝不回显标量值 ============================
//
// 所有对外文案只给"字段名 + 位置 + 原因"，**绝不包含路径或值的原文**。
// 理由：`volumes: - ${SECRET_PATH}:/data` 这种写法里，
// 标量可能被插值成密钥；一旦写进错误信息，它就进了 Hub 返回体、界面和审计日志。
// 行号足以让用户自己定位。

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ImportBlocker 是一条阻塞原因。
//
// 刻意**不含**任何来自用户的原文（路径、值），只含字段名与行号。
type ImportBlocker struct {
	// Service 是出问题的服务名。服务名是用户自己写的标识符，不是密钥，
	// 保留它能让"哪个服务有问题"一目了然；仍做了截断与字符过滤。
	Service string
	// Field 是字段名（我们自己认识的固定词），例如 env_file、build。
	Field string
	// Line 是该节点在文件里的行号（1 起）；0 表示未知。
	Line int
	// Reason 是给用户看的中文说明（我方固定文案）。
	Reason string
}

// String 把阻塞原因格式化成一行文案。
func (b ImportBlocker) String() string {
	var sb strings.Builder
	if b.Line > 0 {
		fmt.Fprintf(&sb, "第 %d 行 ", b.Line)
	}
	if b.Service != "" {
		fmt.Fprintf(&sb, "服务 %s 的 ", b.Service)
	}
	if b.Field != "" {
		fmt.Fprintf(&sb, "%s 字段 ", b.Field)
	}
	sb.WriteString(b.Reason)
	return sb.String()
}

// 首批一律拒绝的字段。语义见各自的原因文案。
//
// 这些共同点：导入后它们引用的外部文件**不会跟着被复制**，
// 于是导入必然产生一份不完整或行为已变的编排。
// 不做"只支持一部分"的模糊兼容——假兼容比明确拒绝更伤人。
var (
	// 顶层整份拒绝：include 合并别的文件、extends 继承别的文件。
	blockingTopLevel = map[string]string{
		"include": "引用了另一份编排文件（include），导入后该文件不会被一起复制，暂不支持导入",
	}
	// 服务级拒绝。
	blockingServiceFields = map[string]string{
		"env_file": "引用了外部环境变量文件（env_file），导入后基准目录改变，该引用会指向错误位置，暂不支持导入",
		"build":    "需要构建上下文（build），导入后相对路径与构建产物都无法保证正确，暂不支持导入",
		"extends":  "继承了另一份编排文件里的服务（extends），导入后该文件不会被一起复制，暂不支持导入",
		"configs":  "引用了配置文件（configs），导入后外部文件不会跟着复制，暂不支持导入",
		"secrets":  "引用了密钥文件（secrets），导入后外部文件不会跟着复制，暂不支持导入",
	}
)

// maxScanBytes 是待扫描内容的字节上限（1MB）。
const maxScanBytes = 1 << 20

// maxScanNodes 是节点数上限，防解析炸弹式的超大文档。
//
// yaml.v3 对别名展开深度有保护，但一份节点极多的文档仍会吃掉大量内存与 CPU。
// 超限时**拒绝导入**而不是放行——拒绝只是让用户改小文件，放行是 Agent 被打死。
const maxScanNodes = 200000

// ScanImportSafety 扫描一份 compose 内容，返回阻塞导入的原因。
//
// 解析失败时返回一条"无法解析"的阻塞（而不是放行）：
// 连结构都读不懂的编排，没法保证导入后行为正确。
//
// **不展开别名**：遇到 Alias / merge 键（<<）直接拒绝（见 scanNode）。
// 首版明确不支持，胜过"展开了但算不清来源"。
func ScanImportSafety(content string) []ImportBlocker {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	if len(content) > maxScanBytes {
		return []ImportBlocker{{
			Reason: fmt.Sprintf("该编排文件过大（超过 %d KB），已拒绝导入", maxScanBytes/1024),
		}}
	}

	dec := yaml.NewDecoder(strings.NewReader(content))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		// **不把 err 的原文带出去**：yaml 的解析错误会附带你写错的那段内容。
		return []ImportBlocker{{
			Reason: "无法解析该 YAML（语法或结构有问题），已拒绝导入",
		}}
	}

	ctx := &scanCtx{}
	ctx.walkStructure(&root)
	if ctx.tooMany {
		return []ImportBlocker{{
			Reason: "该编排文件结构过于复杂（节点数超过上限），为避免占用本机资源已拒绝导入",
		}}
	}
	// 结构上已有拒绝（Alias/merge/tag/无法解析）时不必再报字段级问题：
	// 那时的字段含义本身就是不确定的，再列一批只会让用户困惑。
	if len(ctx.blockers) > 0 {
		return ctx.blockers
	}
	return ctx.scanKnownFields(content)
}

// scanCtx 承载遍历状态。
type scanCtx struct {
	blockers []ImportBlocker
	nodes    int
	tooMany  bool
}

// walkStructure 先整体走一遍，拒掉 Alias / merge / 自定义 tag。
//
// 为什么不只看"已知字段"就完事：
//   - Alias（*name）会让真正的值出现在别处，我们无法在不展开的情况下
//     确定 env_file 是否被引入；展开又可能被用来构造爆炸。
//   - merge（<<）语义同上，而且会把多个块合并进服务定义。
//   - 自定义 tag（!foo）可能改变值的解释方式（例如 !include）。
//
// 三者首版一律拒绝，文案明确告知"暂不支持"，而不是猜测后放行。
func (c *scanCtx) walkStructure(n *yaml.Node) {
	if n == nil || c.tooMany {
		return
	}
	c.nodes++
	if c.nodes > maxScanNodes {
		c.tooMany = true
		return
	}

	switch n.Kind {
	case yaml.AliasNode:
		c.blockers = append(c.blockers, ImportBlocker{
			Line:   n.Line,
			Reason: "使用了 YAML 锚点引用（*别名），无法在导入时安全地确定其真实内容，暂不支持导入",
		})
		return
	case yaml.ScalarNode:
		if n.Tag != "" && n.Tag != "!!str" && n.Tag != "!!int" && n.Tag != "!!bool" &&
			n.Tag != "!!float" && n.Tag != "!!null" && n.Tag != "!!timestamp" &&
			!strings.HasPrefix(n.Tag, "!!") {
			c.blockers = append(c.blockers, ImportBlocker{
				Line:   n.Line,
				Reason: "使用了自定义 YAML 标签，可能会改变字段含义，暂不支持导入",
			})
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind == yaml.ScalarNode && k.Value == "<<" {
				c.blockers = append(c.blockers, ImportBlocker{
					Line:   k.Line,
					Reason: "使用了 YAML 合并键（<<），会把其它块里的字段合并进来，无法安全判定真实配置，暂不支持导入",
				})
				continue
			}
			c.walkStructure(k)
			c.walkStructure(n.Content[i+1])
		}
		return
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, ch := range n.Content {
			c.walkStructure(ch)
		}
		return
	}
}

// scanKnownFields 遍历已知字段，返回阻塞项。
//
// 与 walkStructure 分开：那一步负责"拒绝无法安全解析的构造"，
// 这一步负责"识别会导致导入后行为改变的具体字段"。
// 两步都在**节点树**上做，因此 flow style / 块样式 / 多文档写法一视同仁。
func (c *scanCtx) scanKnownFields(content string) []ImportBlocker {
	dec := yaml.NewDecoder(strings.NewReader(content))
	var root yaml.Node
	if err := dec.Decode(&root); err != nil {
		return nil // 已在 walkStructure 阶段报过"无法解析"
	}

	doc := &root
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return []ImportBlocker{{Reason: "该文件不是一个 YAML 映射（应形如 services: ...），已拒绝导入"}}
	}

	var out []ImportBlocker
	// 顶层：include / secrets / configs / services。
	for i := 0; i+1 < len(doc.Content); i += 2 {
		k, v := doc.Content[i], doc.Content[i+1]
		key := k.Value

		if reason, blocked := blockingTopLevel[key]; blocked {
			out = append(out, ImportBlocker{Field: key, Line: k.Line, Reason: reason})
			continue
		}
		switch key {
		case "secrets", "configs":
			out = append(out, scanTopLevelFileRefs(key, v)...)
		case "services":
			out = append(out, scanServices(v)...)
		}
	}
	return out
}

// scanTopLevelFileRefs 扫描顶层 secrets/configs 里用到 file: 的条目。
func scanTopLevelFileRefs(section string, node *yaml.Node) []ImportBlocker {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	var out []ImportBlocker
	for i := 0; i+1 < len(node.Content); i += 2 {
		entryName, entry := node.Content[i], node.Content[i+1]
		if entry.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(entry.Content); j += 2 {
			fk := entry.Content[j]
			if fk.Value == "file" {
				// 只给条目名（用户自己起的标识符），不回显路径。
				out = append(out, ImportBlocker{
					Service: safeIdent(entryName.Value),
					Field:   section + ".file",
					Line:    fk.Line,
					Reason:  "引用了外部文件（file），导入后该文件不会被一起复制，暂不支持导入",
				})
			}
		}
	}
	return out
}

// scanServices 扫描 services 下的每个服务。
func scanServices(node *yaml.Node) []ImportBlocker {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	var out []ImportBlocker
	for i := 0; i+1 < len(node.Content); i += 2 {
		nameNode, svc := node.Content[i], node.Content[i+1]
		svcName := safeIdent(nameNode.Value)
		if svc.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(svc.Content); j += 2 {
			fk, fv := svc.Content[j], svc.Content[j+1]
			field := fk.Value

			if reason, blocked := blockingServiceFields[field]; blocked {
				out = append(out, ImportBlocker{
					Service: svcName, Field: field, Line: fk.Line, Reason: reason,
				})
				continue
			}
			// bind 挂载的相对路径：volumes 里的 "./x:/y" 或长语法 source: ./x。
			if field == "volumes" {
				out = append(out, scanRelativeMounts(svcName, fv)...)
			}
			// depends_on 若是长语法且带 condition/required，不影响路径，放行。
		}
	}
	return out
}

// scanRelativeMounts 检查 volumes 里的挂载源。
//
// 判定规则（见 isUnprovableMountSource）：
//   - 相对路径（./ ../ . ..）——导入后基准目录会变，指向错误位置；
//   - 含变量插值/命令替换（$ `）——无法证明展开后是绝对路径。
//
// 裸名字（如 "data"）在 compose 里是**命名卷**，不是路径，**允许**：
// 把它当相对路径会让几乎所有编排都被拒，而命名卷导入后语义不变。
//
// 长语法（type: bind, source: ./x）也要覆盖：写法不同但语义相同。
func scanRelativeMounts(service string, node *yaml.Node) []ImportBlocker {
	if node == nil {
		return nil
	}
	var out []ImportBlocker

	relativeReason := "挂载了相对路径（源以 ./ 或 ../ 开头），导入后基准目录改变，该路径会指向错误位置，暂不支持导入"
	interpReason := "挂载源含变量插值（如 ${VAR}）或命令替换，无法在导入时证明它会展开为绝对路径，暂不支持导入"

	// judge 依源文本给出阻塞项；返回 nil 表示可以导入。
	judge := func(src string, line int) *ImportBlocker {
		src = strings.TrimSpace(src)
		// 命名卷（不含路径分隔符、不含插值）直接放行。
		switch {
		case isRelativePath(src):
			return &ImportBlocker{Service: service, Field: "volumes", Line: line, Reason: relativeReason}
		case strings.ContainsAny(src, "$`"):
			return &ImportBlocker{Service: service, Field: "volumes", Line: line, Reason: interpReason}
		}
		return nil
	}

	switch node.Kind {
	case yaml.SequenceNode:
		for _, item := range node.Content {
			switch item.Kind {
			case yaml.ScalarNode:
				// 短语法："源:目标[:模式]"
				src := item.Value
				if idx := strings.IndexByte(src, ':'); idx >= 0 {
					src = src[:idx]
				}
				if b := judge(src, item.Line); b != nil {
					out = append(out, *b)
				}
			case yaml.MappingNode:
				// 长语法：type/source/target。
				// bind 的 source 必须是绝对路径（compose 语义），
				// 因此这里比短语法更严：连裸名字也不行——`source: data`
				// 在 bind 里是相对路径，不是命名卷。
				typ := scalarValue(item, "type")
				srcNode := mapValue(item, "source")
				if srcNode != nil && srcNode.Kind == yaml.ScalarNode {
					src := strings.TrimSpace(srcNode.Value)
					if b := judge(src, srcNode.Line); b != nil {
						out = append(out, *b)
						break
					}
					if typ == "bind" && !isAbsolutePath(src) {
						out = append(out, ImportBlocker{
							Service: service, Field: "volumes", Line: srcNode.Line,
							Reason: "bind 挂载的源必须是绝对路径，当前写法导入后无法保证指向正确位置，暂不支持导入",
						})
					}
				}
			}
		}
	case yaml.MappingNode:
		// 极少数写法直接用映射，按同样规则处理。
		srcNode := mapValue(node, "source")
		if srcNode != nil && srcNode.Kind == yaml.ScalarNode {
			src := strings.TrimSpace(srcNode.Value)
			if b := judge(src, srcNode.Line); b != nil {
				out = append(out, *b)
			} else if scalarValue(node, "type") == "bind" && !isAbsolutePath(src) {
				out = append(out, ImportBlocker{
					Service: service, Field: "volumes", Line: srcNode.Line,
					Reason: "bind 挂载的源必须是绝对路径，当前写法导入后无法保证指向正确位置，暂不支持导入",
				})
			}
		}
	}
	return out
}

// scalarValue 取映射里某个键的标量值（非标量返回空串）。
func scalarValue(node *yaml.Node, key string) string {
	v := mapValue(node, key)
	if v == nil || v.Kind != yaml.ScalarNode {
		return ""
	}
	return strings.TrimSpace(v.Value)
}

// isAbsolutePath 判断是否是明确的绝对路径。
//
// 兼容 Windows 盘符形式（C:\ / C:/）与 UNC（\\server\share），
// 因为这三种写法导入后都不会因基准目录变化而失效。
func isAbsolutePath(p string) bool {
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\\`) {
		return true
	}
	// 盘符：X: 或 X:\ 或 X:/
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// mapValue 取映射里某个键的值节点。
func mapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

// isRelativePath 判断是否是可识别的相对路径。
func isRelativePath(p string) bool {
	return strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || p == "." || p == ".."
}

// isUnprovableMountSource 判断挂载源是否**无法在导入时证明是绝对路径**。
//
// ============================ 为什么含插值就拒 ============================
//
// `- ${DATA}:/data` 的源是 `${DATA}`，字面上不是相对路径，
// 但展开后完全可能是 `./data`。它展开成什么取决于**外部项目当时的 .env
// 与宿主 shell 环境**，而这两者我们都不可能准确复现：
//   - 用户提供的 .env 是"打算写进去的"，不一定是外部项目实际用的那份；
//   - compose 的宿主 shell 环境优先级高于 .env，而 Agent 进程里
//     这些键都被主动剔除了（见 runner.go）；
//   - 还叠加 `:-` `:?` 默认值与 `${A:-${B}}` 嵌套。
//
// 猜错一次的后果是"数据挂到空目录"——服务能起来，但看不到原有数据，
// 而且用户会认为是导入把数据弄丢了。因此含插值一律拒绝。
//
// ============================ 代价（已确认接受） ============================
//
// 这会连 `${DATA}:/data`（DATA 恰好是绝对路径）这种**合法**用法一起拒掉。
// 首批宁可少导入几个，也不要导入一个数据挂错位置的项目。
// 没有安全的中间态：只要允许含 `$` 的源，就必须展开它才能判定，
// 而"正确展开"的前提我们根本无法满足。
//
// 反引号也算：它在 shell 语境里是命令替换，同样使值不可预知。
func isUnprovableMountSource(p string) bool {
	return isRelativePath(p) || strings.ContainsAny(p, "$`")
}

// safeIdent 清洗用户写的标识符（服务名等），用于在文案里引用。
//
// 为什么要清洗：标识符虽然是用户自己起的名字，但没有任何约束能阻止
// 有人把一段密钥写成服务名。因此只保留字母数字与常见符号，
// 超长截断——既保持可读，又不会把可疑内容原样传播出去。
func safeIdent(s string) string {
	const maxLen = 40
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, r)
		case r == '-' || r == '_' || r == '.':
			out = append(out, r)
		default:
			out = append(out, '_')
		}
		if len(out) >= maxLen {
			break
		}
	}
	if len(out) == 0 {
		return ""
	}
	return string(out)
}
