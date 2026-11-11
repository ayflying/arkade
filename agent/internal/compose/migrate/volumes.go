// Package migrate 实现编排跨服务器迁移的 Agent 侧原语。
//
// ============================ 职责边界 ============================
//
// 本包**只做搬运**：把源服务器上"这个编排用到了什么"解析清楚，
// 把卷打包成可读的归档，把收到的归档解进目标卷，把配置落盘。
//
// 它**不知道**存在另一台服务器：目标地址、凭据、阶段编排全在 Hub。
// 这不是偷懒，而是安全边界——Agent 手里有 docker.sock（等价于宿主机 root），
// 若还要持有另一台 Agent 的接入凭据，一台机器被攻破就等于两台。
//
// ============================ 文件划分 ============================
//
//	volumes.go  从 compose 配置解析出该项目用到的卷，区分命名卷 / 外部卷 / bind
//	archive.go  卷归档的导出、读取、释放（结构同构于 task-13 的镜像归档）
//	import.go   接收分块、校验、解包进目标卷（幂等）
//	compose_import.go  把编排配置落盘到目标（不启动）
package migrate

// volumes.go：从 compose 配置解析出"这个编排到底用了哪些卷"。
//
// ============================ 为什么这一步是整个迁移里最容易错的 ============================
//
// 卷的声明方式有五种，语义各不相同，而**其中只有一部分该被迁移**：
//
//	1. 顶层 volumes 声明 + 服务里引用键名
//	   → 真实卷名是 <project>_<key>（compose 自动加前缀），**要迁移**
//	2. 顶层声明里写了 name: xxx
//	   → 真实卷名就是 xxx（不加前缀），**要迁移**
//	3. 顶层声明里写了 external: true
//	   → 卷在 compose 之外创建，名字由外部决定；**要迁移但要单独告知用户**
//	     它是共享卷，迁过去的是一份副本，不保证与别处一致
//	4. 服务里用 bind 挂载（/host/path:/container/path 或 ./rel:/c）
//	   → 这是宿主机目录，**不迁移**，但必须统计并报告
//	5. 服务里的匿名卷（容器内路径，没有 host/volume 源）
//	   → 每次 up 都会新建一个空卷，没有稳定名字，**无从迁移**
//
// 把 4 和 5 静默忽略是最危险的做法：用户以为数据搬过去了，
// 而目标上跑起来是个空目录，且**没有任何报错**。
// 因此本文件不仅返回"要迁的卷"，还必须返回"跳过了什么、为什么"。

import (
	"fmt"
	"sort"

	"gopkg.in/yaml.v3"
)

// VolumeKind 是卷的来源种类。
type VolumeKind int

const (
	// VolumeNamed 是 compose 自动命名的卷（<project>_<key>）。
	VolumeNamed VolumeKind = iota
	// VolumeExplicit 是顶层声明里写了 name: 的卷（名字不加项目前缀）。
	VolumeExplicit
	// VolumeExternal 是 external: true 的卷（在 compose 之外管理）。
	VolumeExternal
)

// Volume 是一个需要迁移的卷。
type Volume struct {
	// Key 是 compose 文件里的声明键名（如 data）。
	Key string
	// Name 是 Docker 里真实存在的卷名（已按规则解析出前缀）。
	Name string
	Kind VolumeKind
	// Services 是引用这个卷的服务名（排序后），用于报告可读性。
	Services []string
}

// SkippedMount 是一个不迁移的挂载。
type SkippedMount struct {
	// Service 是服务名。
	Service string
	// Source 是挂载源（bind 的宿主机路径；匿名卷为空）。
	Source string
	// Target 是容器内路径。
	Target string
	// Reason 是中文原因（直接给用户看）。
	Reason string
}

// Plan 是一个编排的卷迁移计划。
type Plan struct {
	Project string
	// Volumes 是要迁移的命名卷（按名字排序）。
	Volumes []Volume
	// BindMounts 是宿主机目录绑定（不迁移）。
	BindMounts []SkippedMount
	// AnonymousMounts 是匿名卷挂载（不迁移）。
	AnonymousMounts []SkippedMount
}

// HasData 表示这个编排是否声明了任何数据卷。
//
// ============================ 为什么区分"有卷"和"有要迁的卷" ============================
// 用户没勾选迁移卷时，结果里要写"该项目有 N 个数据卷被跳过"。
// 这里的 N 必须是**用户认知里的数据卷数**——即 Volumes 的长度，
// 而不是 bind 挂载数。bind 挂载是宿主机目录，用户不会把它当成"数据卷"。
//
// 若把 bind 也算进去，用户会看到"3 个数据卷被跳过"而实际只有 1 个命名卷，
// 于是以为漏了两个，白折腾一轮。
func (p Plan) DataVolumeCount() int { return len(p.Volumes) }

// VolumeNames 返回要迁移的卷名列表。
func (p Plan) VolumeNames() []string {
	out := make([]string, 0, len(p.Volumes))
	for _, v := range p.Volumes {
		out = append(out, v.Name)
	}
	return out
}

// ResolvePlan 从 compose 配置解析出卷迁移计划。
//
// project 必须是**已经规整过的**项目名（与 cleanName 同一规则），
// 因为这里要用它拼出 <project>_<key>。
func ResolvePlan(project, content string) (Plan, error) {
	plan := Plan{Project: project}

	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		return plan, fmt.Errorf("编排配置无法解析: %w", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		// 空配置没有卷可言，直接返回空计划（不是错误）。
		return plan, nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return plan, fmt.Errorf("编排配置的顶层不是映射结构")
	}

	// ============================ 先建顶层声明表 ============================
	// 顶层 volumes: 段声明了"有哪些卷可用、真实名字是什么"。
	// 服务里的引用只能靠它解析出真实名字。
	decls := resolveTopLevelVolumes(root, project)

	// 引用收集：哪些服务用了哪些卷 / 哪些是 bind / 哪些是匿名。
	refs := make(map[string][]string) // 卷键 → 引用它的服务
	seenBind := make(map[string]struct{})
	seenAnon := make(map[string]struct{})

	services := mapValueNode(root, "services")
	if services != nil && services.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(services.Content); i += 2 {
			svcName := services.Content[i].Value
			svc := services.Content[i+1]
			if svc.Kind != yaml.MappingNode {
				continue
			}
			vols := mapValueNode(svc, "volumes")
			if vols == nil || vols.Kind != yaml.SequenceNode {
				continue
			}
			for _, item := range vols.Content {
				switch item.Kind {
				case yaml.ScalarNode:
					// 短语法：可能是 "name:/path" 或 "/path"（匿名）。
					parseShortMount(project, svcName, item.Value,
						refs, &plan, seenBind, seenAnon)
				case yaml.MappingNode:
					// 长语法：type: volume/bind + source + target。
					parseLongMount(project, svcName, item,
						refs, &plan, seenBind, seenAnon)
				}
			}
		}
	}

	// ============================ 汇总要迁的卷 ============================
	// 只保留**被服务实际引用**的声明：顶层声明了但没人用的卷不迁移，
	// 因为它的内容对这次迁移毫无意义，迁过去只会白占空间。
	for key, svcs := range refs {
		d, ok := decls[key]
		if !ok {
			// 引用了但顶层没声明：compose 会把它当成匿名卷或直接报错。
			// 这里保守地当成"未声明"，记入匿名跳过清单，
			// 绝不猜一个名字去迁（猜错就会去读别人的卷）。
			if _, dup := seenAnon[svcFirst(svcs)+"/"+key]; dup {
				continue
			}
			seenAnon[svcFirst(svcs)+"/"+key] = struct{}{}
			plan.AnonymousMounts = append(plan.AnonymousMounts, SkippedMount{
				Service: svcFirst(svcs),
				Source:  key,
				Reason:  "服务引用的卷没有在顶层 volumes 里声明，无法确定真实卷名",
			})
			continue
		}
		d.Services = normalizeServices(append(d.Services, svcs...))
		plan.Volumes = append(plan.Volumes, d)
	}

	sort.Slice(plan.Volumes, func(i, j int) bool { return plan.Volumes[i].Name < plan.Volumes[j].Name })
	sort.Slice(plan.BindMounts, func(i, j int) bool {
		if plan.BindMounts[i].Service != plan.BindMounts[j].Service {
			return plan.BindMounts[i].Service < plan.BindMounts[j].Service
		}
		return plan.BindMounts[i].Source < plan.BindMounts[j].Source
	})
	sort.Slice(plan.AnonymousMounts, func(i, j int) bool {
		if plan.AnonymousMounts[i].Service != plan.AnonymousMounts[j].Service {
			return plan.AnonymousMounts[i].Service < plan.AnonymousMounts[j].Service
		}
		return plan.AnonymousMounts[i].Source < plan.AnonymousMounts[j].Source
	})
	return plan, nil
}

// resolveTopLevelVolumes 解析顶层 volumes 声明，算出每个键的真实卷名。
func resolveTopLevelVolumes(root *yaml.Node, project string) map[string]Volume {
	out := make(map[string]Volume)
	vols := mapValueNode(root, "volumes")
	if vols == nil || vols.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(vols.Content); i += 2 {
		key := vols.Content[i].Value
		body := vols.Content[i+1]

		v := Volume{Key: key}
		// 默认命名规则：<project>_<key>，且 key 里的非法字符会被规整。
		v.Name = project + "_" + normalizeVolumeKey(key)
		v.Kind = VolumeNamed

		if body.Kind == yaml.MappingNode {
			// external: true → 卷由外部管理，名字必须显式给出（或不加前缀）。
			if ext := mapValueNode(body, "external"); ext != nil {
				isExternal := ext.Value == "true"
				// external 可以是布尔，也可以是带 name 的映射（老语法）。
				if ext.Kind == yaml.MappingNode {
					isExternal = true
					if n := mapValueNode(ext, "name"); n != nil && n.Value != "" {
						v.Name = n.Value
					}
				}
				if isExternal {
					v.Kind = VolumeExternal
					// external 卷**不加项目前缀**，用键名本身。
					if v.Name == project+"_"+normalizeVolumeKey(key) {
						v.Name = key
					}
				}
			}
			// name: xxx → 显式命名，覆盖默认前缀规则。
			if n := mapValueNode(body, "name"); n != nil && n.Value != "" {
				v.Name = n.Value
				v.Kind = VolumeExplicit
				// external + name 同时出现时，external 的语义优先
				// （卷确实是外部管理的），但名字取 name: 的值。
				if ext := mapValueNode(body, "external"); ext != nil {
					if ext.Value == "true" || ext.Kind == yaml.MappingNode {
						v.Kind = VolumeExternal
					}
				}
			}
		}
		out[key] = v
	}
	return out
}
