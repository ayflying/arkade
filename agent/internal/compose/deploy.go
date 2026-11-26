package compose

// deploy.go：「更新部署」——先检查镜像有没有更新，只有确实有更新才拉取并重建。
//
// ============================ 为什么这个动作与 pull / rebuild 不同 ============================
//
// pull 只拉镜像，容器仍跑旧版本（界面得再点一次重建）；
// rebuild 无论有没有新镜像都会 pull + 强制重建，白等一次拉取、白停一次服务。
//
// 用户真正要的是「有新版本就更新，没有就别动」。这个意图无法靠一条固定命令完成，
// 必须自己判定，因此它是一段编排流程，而不是一条 compose 子命令：
//
//	第 1 步  docker compose config --format json  解析有效服务和镜像（输出不展示）
//	         docker compose ps + docker inspect   读取容器实际镜像（输出不展示）
//	第 2 步  docker manifest inspect <引用>    只查远端元数据，不在本机产生镜像（输出不展示）
//	第 3 步  docker compose pull <服务名>      只拉候选镜像对应的服务
//	第 4 步  docker compose up -d <该服务>     只重建有更新的
//
// ============================ 安全边界 ============================
//
//  1. 参数由 Agent 拼死：用户只提供项目名，镜像引用取自 compose 正文；
//  2. 不经过 shell（exec(argv)），没有注入面；
//  3. 没有新版本时**不执行 pull / up**，也就不会重启任何容器；
//  4. 第 3 步任一服务拉取失败就停在这里，绝不继续重建——
//     「拉取失败却用旧镜像重建」会让服务白停一次，而用户以为已经更新；

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// 对外文案（用户看到的就是这些字）。
//
// 集中成常量：字面量散落在流程里时，改一句就得全项目搜。
const (
	deployNoUpdateText = "没有可更新的镜像，未拉取镜像，也未重建任何容器。"
	// deployAllUnknownText 用于「所有引用镜像的仓库都查不到」。
	//
	// 这时**不能**说"已是最新"——那是没有证据的结论。
	// 但也不能拒绝执行：私有仓库、离线环境里查不到摘要很常见，
	// 拒绝会让更新部署完全不可用。因此退化为"按拉取后的实际内容判断"并明说。
	deployAllUnknownText = "镜像仓库没有返回可对比的摘要，无法在拉取前判断是否有新版本；" +
		"本次只按拉取后的实际内容判断，没有新内容时不会重建容器。"
)

// deployOutcome 是一次更新部署的结论（写入任务快照）。
type deployOutcome struct {
	// UpdatedServices 是本轮真正重建过的服务（去重、排序）。
	UpdatedServices []string
	// ChangedImages 是本轮真正拉到新内容的镜像引用（去重、排序）。
	ChangedImages []string
}

// deployHunk 是一个需要更新的镜像的判定结果。
type deployHunk struct {
	// ref 是配置里声明的镜像引用，也是 pull / manifest 的目标。
	ref string
	// id 是远端清单身份；零值表示查不到（退化路径）。
	//
	// ============================ 为什么存结构体而不是拼成一个字符串 ============================
	//
	// 摘要与镜像 ID **都以 "sha256:" 开头**，拼成字符串后就再也分不清哪个是哪个。
	// 而它们的比较方式完全不同：摘要要按"仓库@摘要"比对本机 RepoDigests，
	// 镜像 ID 是直接与容器的 Image 比。混在一起会把"本地构建的镜像 ID"
	// 当成摘要去查 RepoDigests，永远查不到 → 每次都判定"有更新" → 每次都白重建。
	id manifestIdentity
}

// Deploy 执行「更新部署」：仅在有新镜像时拉取并重建对应服务。
//
// 返回的错误一律是"给用户看的一句话"：本函数不把引擎原文塞进错误，
// 命令输出已经通过流式事件展示（且已脱敏），错误里只需要结论与下一步。
func (m *Manager) Deploy(ctx context.Context, name string) (deployOutcome, error) {
	n, err := cleanName(name)
	if err != nil {
		return deployOutcome{}, err
	}

	var out deployOutcome
	var derr error
	lerr := m.withProject(ctx, n, func() error {
		out, derr = m.deployLocked(ctx, n)
		return nil
	})
	if lerr != nil {
		return deployOutcome{}, lerr
	}
	return out, derr
}

// deployLocked 是加锁内的实现。
//
// 业务错误以返回值带出（闭包返回 nil）是刻意的：外层只负责锁，
// 任务层要看到的是"已经跑过一段命令的失败原因"。
func (m *Manager) deployLocked(ctx context.Context, n string) (deployOutcome, error) {
	if err := m.requireExists(n); err != nil {
		return deployOutcome{}, err
	}
	out := deployOutcome{}

	m.deployNote(ctx, "正在检查本机容器和镜像仓库是否有更新…")

	// 用和 pull/up 相同的项目目录、.env 和运行环境解析一次，由 Compose 决定有效服务及镜像。
	decls, err := m.deployServiceImages(ctx, n)
	if err != nil {
		return out, err
	}
	images := serviceImages(decls)
	if len(images) == 0 {
		return out, safeErr("编排里没有找到可更新的服务（已启用的服务没有 image: 声明）")
	}

	// ============================ 第 1 步：现状 ============================
	//
	// 用 probe 而不是 runStream：ps 的 JSON 是给这里解析的，
	// 展示出来只是几百行噪声，还会把真正的进度挤出日志配额；
	// 而流式切分又会撞上单行上限（JSON 是单行），解析必然失败。
	ps := m.probe(ctx, n, TimeoutShort, "ps", "--format", "json", "-a")
	if ps.ExitCode != 0 {
		return deployOutcome{}, safeErr("读取本机容器状态失败（退出码 %d），未做任何改动", ps.ExitCode)
	}
	entries := parsePSJson(ps.Stdout)
	m.inspectDeployImages(ctx, n, entries)
	running := runningLocalImages(entries)

	// ============================ 第 2 步：对比远端 ============================
	hunks, unknown := m.planDeploy(ctx, n, images, running)
	if len(unknown) > 0 && len(unknown) < len(images) {
		m.deployNote(ctx, unknownText(servicesOf(decls, selfHunks(unknown))))
	}

	toPull := hunks
	if len(hunks) == 0 {
		if len(unknown) != len(images) {
			m.deployNote(ctx, deployNoUpdateText)
			return out, nil
		}
		// 全部查不到：退化为"只拉取、按实际内容更新"，不能假装已是最新。
		m.deployNote(ctx, deployAllUnknownText)
		toPull = selfHunks(images)
	}

	// ============================ 第 3 步：只拉有更新的 ============================
	//
	// 失败立即停：拉取失败还继续重建，会让服务白停一次而用户以为已经更新。
	if !m.deployPull(ctx, n, decls, toPull) {
		return out, safeErr("拉取镜像失败，已停止后续重建；正在运行的容器没有改动")
	}

	// ============================ 第 4 步：只重建确实换了内容的 ============================
	wanted := make(map[string]struct{}, len(toPull))
	for _, h := range toPull {
		wanted[h.ref] = struct{}{}
	}
	pulled := make(map[string]string, len(wanted))
	for ref := range wanted {
		pulled[ref] = m.localImageID(ctx, n, ref)
	}
	var names, unverified []string
	for _, d := range decls {
		if _, ok := wanted[d.image]; !ok {
			continue
		}
		changed, verified := deployServiceChange(entries, d.service, pulled[d.image])
		if !verified {
			unverified = append(unverified, d.service)
		} else if changed {
			names = append(names, d.service)
			out.ChangedImages = append(out.ChangedImages, d.image)
		}
	}
	out.ChangedImages = dedupeSorted(out.ChangedImages)
	if len(unverified) > 0 {
		m.deployNote(ctx, fmt.Sprintf("%s：无法核实实际镜像身份，未重建。", strings.Join(unverified, "、")))
	}
	if len(names) == 0 {
		if len(unverified) == 0 {
			m.deployNote(ctx, "拉取到的镜像与本机一致；未重建任何容器。")
		}
		return out, nil
	}

	// 已验证镜像 ID 变更，让 Compose 正常替换一次，不连带启动依赖，
	// 不再拉取或构建，以免执行对象偏离刚核验的本机镜像。
	upArgs := append([]string{"up", "-d", "--no-deps", "--no-build", "--pull", "never", "--"}, names...)
	if up := m.runStream(ctx, n, TimeoutLong, quietSink(), upArgs...); up.ExitCode != 0 {
		return out, safeErr("用新镜像重建 %s 失败，请查看任务输出和容器状态", strings.Join(names, "、"))
	}

	out.UpdatedServices = names
	m.deployNote(ctx, fmt.Sprintf("更新完成：已重建 %s，共 %d 个服务。",
		strings.Join(names, "、"), len(names)))
	return out, nil
}

// planDeploy 逐引用判定是否需要更新。
//
// 返回（需要更新的条目, 无法判定的引用）。每条判定都基于**本机正在运行的那个容器**：
//   - 本机没有运行中的容器 → 视为需要更新（首次部署交给 up）；
//   - 本机 RepoDigests 里已有远端 digest → 是最新，跳过；
//   - 远端只给镜像 ID 且等于本机镜像 ID → 是最新，跳过；
//   - 查不到远端身份 → 无法判定，**不进入 pull 列表**。
//
// 按引用而不是按服务聚合：同一个镜像被多个服务共用时，
// 远端只应查一次、判定也只做一次；服务名在第 4 步才做反查。
func (m *Manager) planDeploy(ctx context.Context, n string, images []string,
	running map[string]localImage) ([]deployHunk, []string) {

	return planDeployWith(images, running, func(ref string) manifestIdentity {
		return m.inspectManifest(ctx, n, ref)
	})
}

// planDeployWith 是判定逻辑的**纯函数**部分（远端查询由调用方注入）。
//
// ============================ 为什么要把查询注入进来 ============================
//
// "要不要更新"是这套功能里最容易出错、后果最大的一步：
// 判成"有更新"就会白拉一遍并重启容器；判成"没更新"就会让用户以为已经是最新。
// 把它做成纯函数后，这张判定表可以在任何平台上被穷举验证，
// 而不必依赖"在 Linux 上跑一个假 docker 脚本"。
func planDeployWith(images []string, running map[string]localImage,
	inspect func(ref string) manifestIdentity) ([]deployHunk, []string) {

	unknown := make([]string, 0, len(images))
	hunks := make([]deployHunk, 0, len(images))

	for _, ref := range images {
		local, hasContainer := running[ref]
		if !hasContainer {
			hunks = append(hunks, deployHunk{ref: ref})
			continue
		}
		id := inspect(ref)
		if !id.known() {
			unknown = append(unknown, ref)
			continue
		}
		if id.digest != "" && hasRepoDigest(local.digests, ref, id.digest) {
			continue
		}
		if id.digest == "" && id.imageID != "" && local.imageID == id.imageID {
			continue
		}
		hunks = append(hunks, deployHunk{ref: ref, id: id})
	}
	return hunks, dedupeSorted(unknown)
}

// deployPull 只拉给定的引用，任一失败即返回 false。
//
// 拉取的输出**要展示**（用户需要看到每一层在拉、拉了多少），
// 因此这里用普通 ctx + quietSink：sink 吞掉自己的副本，
// 而观察者由 RunStream 从 ctx 取出，照样把每一行推进任务日志。
func (m *Manager) deployPull(ctx context.Context, n string, decls []serviceTag, hunks []deployHunk) bool {
	byImage := make(map[string][]string)
	for _, decl := range decls {
		byImage[decl.image] = append(byImage[decl.image], decl.service)
	}
	seen := make(map[string]struct{}, len(hunks))
	for _, h := range hunks {
		if h.ref == "" {
			continue
		}
		if _, dup := seen[h.ref]; dup {
			continue
		}
		seen[h.ref] = struct{}{}
		// Compose pull 接受服务名，不接受镜像引用。保留 Compose 上下文，
		// 让它按对应服务的 platform 等配置拉取；不能退回裸 pull。
		services := dedupeSorted(byImage[h.ref])
		if len(services) == 0 {
			return false
		}
		args := append([]string{"pull", "--"}, services...)
		if res := m.runStream(ctx, n, TimeoutLong, quietSink(), args...); res.ExitCode != 0 {
			return false
		}
	}
	return true
}

// selfHunks 把引用列表转成只有引用的条目（用于"全部无法判定"的退化路径）。
func selfHunks(images []string) []deployHunk {
	out := make([]deployHunk, 0, len(images))
	for _, ref := range images {
		out = append(out, deployHunk{ref: ref})
	}
	return out
}

// servicesOf 反查这些镜像引用对应的服务名（去重、排序）。
func servicesOf(decls []serviceTag, hunks []deployHunk) []string {
	if len(hunks) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(hunks))
	for _, h := range hunks {
		want[h.ref] = struct{}{}
	}
	names := make([]string, 0, len(hunks))
	for _, d := range decls {
		if _, ok := want[d.image]; ok {
			names = append(names, d.service)
		}
	}
	return dedupeSorted(names)
}

// deployNote 往任务输出里写一条阶段说明（没有对应 argv，因此用 stage 流）。
func (m *Manager) deployNote(ctx context.Context, text string) {
	if ob := observerFrom(ctx); ob != nil {
		ob.stage(text, nil)
	}
}

// unknownText 只点出无法判定的服务，不回显插值后的镜像引用。
func unknownText(services []string) string {
	return fmt.Sprintf("以下服务的镜像无法从仓库取得摘要，本次保持原样、不拉取也不重建：%s。"+
		"（私有仓库可能需要先登录，或该镜像不是从仓库拉取的）", strings.Join(services, "、"))
}

// dedupeSorted 去重并升序排序（空串丢弃）。
func dedupeSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
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
