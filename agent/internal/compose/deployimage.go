package compose

// deployimage.go：更新部署用到的两件"读"的本事。
//
//   1. 用 Compose 规范化配置，取出每个服务实际使用的镜像引用；
//   2. 查远端清单身份（manifest inspect），并把本机容器与其镜像摘要对齐。
//
// 单独成文的原因：这里全是**证据采集**，不含任何"要不要重建"的判断。
// 判断在 deploy.go，采集在这里——两边混在一起时，
// 一改解析就会牵动重启决策，而那是最不该被动的地方。

import (
	"context"
	"encoding/json"
	"runtime"
	"sort"
	"strings"
)

// serviceTag 是"服务名 + 镜像引用"的一条声明。
type serviceTag struct {
	service string
	image   string
}

// deployServiceImages 让 Compose 自己处理缩进、合并、.env 插值、profiles 等。
func (m *Manager) deployServiceImages(ctx context.Context, name string) ([]serviceTag, error) {
	// 规范化配置可能包含展开后的凭据。stdout 和 stderr 都只能留在本地，
	// 也不能把 JSON 解析错误或 Compose 的错误原文放进任务结果。
	res := m.probe(ctx, name, TimeoutShort, "config", "--format", "json")
	if res.ExitCode != 0 {
		return nil, safeErr("解析编排配置失败（退出码 %d），未做任何改动", res.ExitCode)
	}
	decls, ok := parseDeployServiceImages(res.Stdout)
	if !ok {
		return nil, safeErr("Compose 返回的编排配置无效，未做任何改动")
	}
	return decls, nil
}

func parseDeployServiceImages(raw string) ([]serviceTag, bool) {
	var config struct {
		Services map[string]struct {
			Image string `json:"image"`
		} `json:"services"`
	}
	if json.Unmarshal([]byte(raw), &config) != nil || config.Services == nil {
		return nil, false
	}
	decls := make([]serviceTag, 0, len(config.Services))
	for name, svc := range config.Services {
		if ref := strings.TrimSpace(svc.Image); ref != "" {
			decls = append(decls, serviceTag{service: name, image: ref})
		}
	}
	// JSON 对象无序，先按服务名排序，镜像检查及拉取顺序才能稳定。
	sort.Slice(decls, func(i, j int) bool { return decls[i].service < decls[j].service })
	return decls, true
}

func serviceImages(decls []serviceTag) []string {
	seen := make(map[string]struct{}, len(decls))
	out := make([]string, 0, len(decls))
	for _, d := range decls {
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
