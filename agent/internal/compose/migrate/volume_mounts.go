package migrate

import (
	"gopkg.in/yaml.v3"
	"sort"
	"strings"
)

// parseShortMount 解析短语法挂载 "source:target[:ro]"。
//
// 短语法的歧义是 compose 里出了名的坑：
//
//	"/abs/path:/data"  → bind（宿主机绝对路径）
//	"./rel:/data"      → bind（相对路径）
//	"data:/data"       → 命名卷引用
//	"/data"            → 匿名卷（只有一个路径）
//
// 判断依据是"第一个冒号前是否是路径"。这里用 isBindSource 统一判断，
// 不做两处不同的猜测。
func parseShortMount(project, service, spec string,
	refs map[string][]string, plan *Plan,
	seenBind, seenAnon map[string]struct{}) {
	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 1:
		// 只有容器内路径 → 匿名卷。
		target := strings.TrimSpace(parts[0])
		k := service + "/" + target
		if _, dup := seenAnon[k]; dup {
			return
		}
		seenAnon[k] = struct{}{}
		plan.AnonymousMounts = append(plan.AnonymousMounts, SkippedMount{
			Service: service, Target: target,
			Reason: "匿名卷（未指定来源），每次启动都会新建空卷，无法迁移",
		})
	case 2, 3:
		src := strings.TrimSpace(parts[0])
		target := strings.TrimSpace(parts[1])
		if isBindSource(src) {
			k := service + "/" + src
			if _, dup := seenBind[k]; dup {
				return
			}
			seenBind[k] = struct{}{}
			plan.BindMounts = append(plan.BindMounts, SkippedMount{
				Service: service, Source: src, Target: target,
				Reason: "宿主机目录绑定（bind），迁移不会复制宿主机上的目录内容",
			})
			return
		}
		refs[src] = append(refs[src], service)
	}
}

// parseLongMount 解析长语法挂载（type/source/target 映射）。
func parseLongMount(project, service string, node *yaml.Node,
	refs map[string][]string, plan *Plan,
	seenBind, seenAnon map[string]struct{}) {
	mountType := ""
	if n := mapValueNode(node, "type"); n != nil {
		mountType = n.Value
	}
	source := ""
	if n := mapValueNode(node, "source"); n != nil {
		source = n.Value
	}
	target := ""
	if n := mapValueNode(node, "target"); n != nil {
		target = n.Value
	}

	// type 显式给出时以它为准（它比启发式判断可靠）。
	switch mountType {
	case "bind", "tmpfs", "npipe", "cluster":
		k := service + "/" + source + "/" + target
		if _, dup := seenBind[k]; dup {
			return
		}
		seenBind[k] = struct{}{}
		reason := "宿主机目录绑定（bind），迁移不会复制宿主机上的目录内容"
		if mountType == "tmpfs" {
			reason = "tmpfs 内存挂载，本来就不保存数据"
		}
		plan.BindMounts = append(plan.BindMounts, SkippedMount{
			Service: service, Source: source, Target: target, Reason: reason,
		})
		return
	case "volume":
		if source == "" {
			k := service + "/" + target
			if _, dup := seenAnon[k]; dup {
				return
			}
			seenAnon[k] = struct{}{}
			plan.AnonymousMounts = append(plan.AnonymousMounts, SkippedMount{
				Service: service, Target: target,
				Reason: "匿名卷（type=volume 但未指定 source），无法确定真实卷名",
			})
			return
		}
		refs[source] = append(refs[source], service)
		return
	}

	// 没有 type 字段（老写法或省略）：按 source 的形态判断。
	if source == "" {
		k := service + "/" + target
		if _, dup := seenAnon[k]; dup {
			return
		}
		seenAnon[k] = struct{}{}
		plan.AnonymousMounts = append(plan.AnonymousMounts, SkippedMount{
			Service: service, Target: target,
			Reason: "未指定来源的挂载，无法确定真实卷名",
		})
		return
	}
	if isBindSource(source) {
		k := service + "/" + source
		if _, dup := seenBind[k]; dup {
			return
		}
		seenBind[k] = struct{}{}
		plan.BindMounts = append(plan.BindMounts, SkippedMount{
			Service: service, Source: source, Target: target,
			Reason: "宿主机目录绑定（bind），迁移不会复制宿主机上的目录内容",
		})
		return
	}
	refs[source] = append(refs[source], service)
}

// isBindSource 判断挂载源是否是宿主机路径。
//
// 规则：
//   - 以 / 或 ./ 或 ../ 开头 → 路径；
//   - Windows 盘符（C:\ 或 C:/）→ 路径；
//   - 其他 → 卷名（Docker 卷名只允许 [a-zA-Z0-9][a-zA-Z0-9_.-]*，不含斜杠）。
//
// 关键点：**卷名不可能包含斜杠**，因此"含斜杠即路径"这条判断是充分的。
// 不这么做的话，像 "my-data:/data" 这种合法卷名会被误判成路径。
func isBindSource(src string) bool {
	if src == "" {
		return false
	}
	if strings.HasPrefix(src, "/") || strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
		return true
	}
	// Windows 绝对路径：盘符 + 冒号或反斜杠。
	if len(src) >= 2 && src[1] == ':' {
		c := src[0]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	if strings.Contains(src, "\\") {
		return true
	}
	return false
}

// normalizeVolumeKey 把卷键规整成 compose 会用的卷名片段。
//
// compose 把卷名里的非法字符替换成 _，并对小写化有要求。
// 这里只处理最常见的情况：非 [a-z0-9_.-] 的字符替换成 _。
func normalizeVolumeKey(key string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// normalizeServices 去重并排序服务名。
func normalizeServices(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// svcFirst 返回第一个服务名（用于错误文案）。
func svcFirst(svcs []string) string {
	if len(svcs) == 0 {
		return ""
	}
	return svcs[0]
}

// mapValueNode 在映射节点里按键取值（yaml.v3 的映射是扁平 [k1,v1,k2,v2]）。
func mapValueNode(node *yaml.Node, key string) *yaml.Node {
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
