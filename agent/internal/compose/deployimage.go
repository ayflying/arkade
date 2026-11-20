package compose

// deployimage.go：更新部署用到的两件"读"的本事。
//
//   1. 从 compose 正文里取出每个服务声明的镜像引用（serviceDecls / serviceImages）；
//   2. 查远端清单身份（manifest inspect），并把本机容器与其镜像摘要对齐。
//
// 单独成文的原因：这里全是**证据采集**，不含任何"要不要重建"的判断。
// 判断在 deploy.go，采集在这里——两边混在一起时，
// 一改解析就会牵动重启决策，而那是最不该被动的地方。

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
)

// serviceTag 是"服务名 + 镜像引用"的一条声明。
type serviceTag struct {
	service string
	image   string
}

// serviceDecls 按正文顺序返回每个服务声明的镜像引用。
//
// 只认 services 块里"比服务名深一层"的 image: 行，与 ParseServices 用同一套
// 缩进规则（indentWidth / keyName / stripComment）：两处若各写一份缩进判断，
// 迟早会一边认一边不认，出现"列表里有两个服务、更新时只看到一个"的错觉。
func serviceDecls(content string) []serviceTag {
	lines := strings.Split(content, "\n")

	var (
		out           = make([]serviceTag, 0, 8)
		inServices    = false
		serviceIndent = -1
		current       = ""
		declared      = map[string]struct{}{}
	)

	for _, raw := range lines {
		line := stripComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := indentWidth(line)
		trimmed := strings.TrimSpace(line)

		if !inServices {
			if indent == 0 && isKey(trimmed, "services") {
				inServices = true
			}
			continue
		}
		if indent == 0 {
			break // 回到顶格：services 块结束
		}

		// ============================ 这里不能"猜"服务名的缩进 ============================
		//
		// 直觉写法是把第一次见到的缩进当成服务层，但它有个静默 bug：
		// 只要第一行恰好是更深的内容（行内映射的续行、被缩进的注释后的内容等），
		// 真正的服务名就会被当成"更深一层的属性"而**全部漏掉**，
		// 表现为"编排里没有找到可更新的服务"。
		//
		// 因此判定顺序反过来：先认"这一行像不像 image: 属性（服务名缩进 + 2）"，
		// 再认"这一行是不是一个新的服务名（任意缩进，只要合法）"。
		// 两类的区别只在"是否以合法服务名的 key 开头"，互不干扰。
		if serviceIndent >= 0 && indent == serviceIndent+2 && isKey(trimmed, "image") {
			if current == "" {
				continue
			}
			if _, dup := declared[current]; dup {
				continue
			}
			declared[current] = struct{}{}
			if value := imageValue(trimmed); value != "" {
				out = append(out, serviceTag{service: current, image: value})
			}
			continue
		}

		if name, ok := keyName(trimmed); ok && ValidateService(name) == nil {
			if serviceIndent < 0 {
				serviceIndent = indent // 第一个服务名定下服务层缩进
			}
			current = name
			continue
		}
	}
	return out
}

// imageValue 取出 `image: xxx` 里的引用值（去引号、去空）。
//
// 含 `${...}` 插值的一律视为"取不到"：真实引用要由 compose 结合 .env 与 shell
// 环境求值，Agent 里无法忠实重现。宁可让它落进"无法判定"，
// 也不要拿一个带花括号的字符串去 pull——那只会报一个看不懂的错。
func imageValue(trimmedLine string) string {
	value := strings.TrimSpace(strings.TrimPrefix(trimmedLine, "image:"))
	value = strings.Trim(value, `"'`)
	if value == "" || strings.Contains(value, "${") || strings.ContainsAny(value, " \t") {
		return ""
	}
	return value
}

// serviceImages 返回去重后的镜像引用（保持正文出现顺序）。
func serviceImages(content string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 8)
	for _, d := range serviceDecls(content) {
		if _, dup := seen[d.image]; dup {
			continue
		}
		seen[d.image] = struct{}{}
		out = append(out, d.image)
	}
	return out
}

// localImage 是本机容器与其镜像摘要的对齐关系。
type localImage struct {
	// imageID 是容器当前使用的镜像 ID（docker inspect 的 Image 字段）。
	imageID string
	// digests 是该镜像本机记录的 RepoDigests（形如 repo@sha256:...）。
	digests []string
}

// runningLocalImages 把 compose ps 的结果整理成 引用 → 本机镜像。
//
// 同一条引用跑着多个副本时取第一个：它们应当指向同一份内容；
// 真不一致（部分副本已更新）时"至少有一个是旧的"，
// 取第一个正好会判为"需要更新"，方向是安全的。
func runningLocalImages(entries []psEntry) map[string]localImage {
	out := make(map[string]localImage, len(entries))
	for _, e := range entries {
		ref := strings.TrimSpace(e.Image)
		if ref == "" {
			continue
		}
		if _, dup := out[ref]; dup {
			continue
		}
		out[ref] = localImage{imageID: strings.TrimSpace(e.ImageID), digests: e.RepoDigests}
	}
	return out
}

// hasRepoDigest 判断本机的摘要列表里是否已经有 引用@digest。
//
// 按"仓库 + digest"比对而不是只比 digest：同一个 digest 可能同时被
// mirror 前缀与本名记录，但用户关心的是**这个引用**是否已是最新。
func hasRepoDigest(digests []string, ref, digest string) bool {
	if digest == "" {
		return false
	}
	repo := repoOf(ref)
	for _, d := range digests {
		at := strings.LastIndex(d, "@")
		if at <= 0 || d[at+1:] != digest {
			continue
		}
		if repo == "" || repoOf(d[:at]) == repo {
			return true
		}
	}
	return false
}

// manifestIdentity 是远端清单的身份。
//
// 两种形态并存是现实：有 registry 可查时给 digest；本地构建/导入的镜像
// 没有仓库可查，只能靠镜像配置 ID。二者任一能对上就够用。
type manifestIdentity struct {
	digest  string
	imageID string
}

// identity 返回用于"拉取前后是否变了"的稳定标识。
func (m manifestIdentity) identity() string {
	if m.digest != "" {
		return m.digest
	}
	return m.imageID
}

// known 表示远端身份是否拿到了。
func (m manifestIdentity) known() bool { return m.digest != "" || m.imageID != "" }

// inspectManifest 查询远端清单身份（读不到返回零值，不返回错误）。
//
// 为什么吞掉错误：查不到清单是**常态**（私有仓库未登录、离线、镜像已删除、
// 引用是本地构建产物）。把它当失败会让"更新部署"在这些环境里彻底不可用；
// 真正的结论由调用方按"无法判定"处理，并在日志里点名哪些镜像查不到。
//
// 用 probeDocker：manifest inspect 是 docker 本体的子命令（不是 compose 的），
// 且它的 JSON 要**精确解析**、几百行清单对用户没有意义。
func (m *Manager) inspectManifest(ctx context.Context, name, ref string) manifestIdentity {
	res := m.probeDocker(ctx, name, TimeoutManifest, "manifest", "inspect", ref)
	if res.ExitCode != 0 {
		return manifestIdentity{}
	}
	return parseManifestIdentity(res.Stdout)
}

// manifestEntry 是 manifest 列表里的一条。
type manifestEntry struct {
	Digest    string `json:"digest"`
	MediaType string `json:"mediaType"`
	Platform  struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	} `json:"platform"`
}

// isNestedIndex 判断列表里这一条本身还是"清单列表"（多平台嵌套）。
func (m manifestEntry) isNestedIndex() bool {
	return m.MediaType == "application/vnd.docker.distribution.manifest.list.v2+json" ||
		m.MediaType == "application/vnd.oci.image.index.v1+json"
}

// parseManifestIdentity 解析 docker manifest inspect 的输出。
//
// 两种形态：
//   - 清单列表 / OCI index：{"manifests":[{"digest":"sha256:..","platform":{...}}, ...]}
//   - 单清单：{"config":{"digest":"sha256:.."}}
//
// 镜像列表按本机平台挑：不挑就会把 arm64 的 digest 当成"最新"，
// 然后在 amd64 上永远判定"有更新"，每次都白拉一遍。
func parseManifestIdentity(raw string) manifestIdentity {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return manifestIdentity{}
	}

	var list struct {
		Manifests []manifestEntry `json:"manifests"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err == nil && len(list.Manifests) > 0 {
		platforms := localPlatforms()
		// 两轮：先按本机平台找，找不到再退回任意一条**非嵌套 index** 的清单。
		//
		// 为什么两轮而不是"每轮各挑一个"：
		//   - 直接退回第一条会把嵌套 index 的 digest 当成镜像 digest，
		//     那个 digest 与本机 RepoDigests 永远不会相等 → 永远判定"有更新"；
		//   - 因此第二轮也必须跳过嵌套 index，否则等于没修。
		//
		// 平台不匹配（用户拉的是另一种架构的镜像）时仍要给出 digest：
		// 不能因为"本机架构上没有"就说没有更新——镜像确实在上游变了。
		for _, entry := range list.Manifests {
			if entry.isNestedIndex() || entry.Digest == "" {
				continue
			}
			if platforms.match(entry.Platform.OS, entry.Platform.Architecture) {
				return manifestIdentity{digest: strings.TrimSpace(entry.Digest)}
			}
		}
		for _, entry := range list.Manifests {
			if entry.isNestedIndex() || entry.Digest == "" {
				continue
			}
			return manifestIdentity{digest: strings.TrimSpace(entry.Digest)}
		}
	}

	var single struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
		ID string `json:"Id"`
	}
	if err := json.Unmarshal([]byte(raw), &single); err == nil {
		if d := strings.TrimSpace(single.Config.Digest); d != "" {
			return manifestIdentity{digest: d}
		}
		if id := strings.TrimSpace(single.ID); id != "" {
			return manifestIdentity{imageID: id}
		}
	}
	return manifestIdentity{}
}

// platformSet 是本机可接受的镜像平台集合。
//
// 集合里放多个是为了兼容 32 位 ARM 的别名（armhf / armv7 / arm/v7）。
type platformSet struct {
	os    string
	archs []string
}

// match 判断某个清单平台是否属于本机。
func (p platformSet) match(osName, arch string) bool {
	if !strings.EqualFold(strings.TrimSpace(osName), p.os) {
		return false
	}
	arch = strings.ToLower(strings.TrimSpace(arch))
	for _, a := range p.archs {
		if a == arch {
			return true
		}
	}
	return false
}

// localPlatforms 由编译期平台推导（发布产物按目标平台构建，正是镜像选择依据）。
func localPlatforms() platformSet {
	osName := strings.ToLower(runtime.GOOS)
	arch := strings.ToLower(runtime.GOARCH)
	switch arch {
	case "arm":
		// Go 的 GOARCH=arm 覆盖 armv6/v7，registry 上常见三种写法。
		return platformSet{os: osName, archs: []string{"arm", "armhf", "armv7"}}
	case "386":
		return platformSet{os: osName, archs: []string{"386", "i386", "i686"}}
	default:
		return platformSet{os: osName, archs: []string{arch}}
	}
}

// repoOf 取出镜像引用里"去掉 tag/digest"的仓库部分。
//
// 例：ghcr.io/a/b:1.2 → ghcr.io/a/b；nginx:1.25 → nginx。
// 只在两边都走同一个函数时比较，因此不需要实现完整的 registry 规范化规则
// （隐式 docker.io/library 前缀在两边都会同样缺失，比较仍然成立）。
func repoOf(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	// tag 只可能出现在最后一个路径段：ghcr.io/a/b:tag 的冒号在最后一段，
	// 而 localhost:5000/x 的冒号在第一段，不能一概切掉。
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref[slash+1:], ":"); colon >= 0 {
		return ref[:slash+1+colon]
	}
	return ref
}
