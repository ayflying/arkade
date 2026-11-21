package runtime

// selfupdate.go：更新 **Agent 自己所在容器** 的镜像。
//
// ============================ 为什么需要"一次性容器" ============================
//
// 更新 Arkade 自己 = 用新镜像重建自己所在的容器。但执行重建的进程
// 就住在这个容器里：`compose up` 一开始停旧容器，进程立刻被杀掉，
// 于是命令执行到一半就没人管了——既拿不到退出码，也没人清理。
//
// 因此把"重建"这一步交给一个**一次性容器**：它由引擎创建、独立于我方容器，
// 我方被杀掉之后它仍会跑完。它只跑一条写死的 compose 命令。
//
// ============================ 安全边界 ============================
//
// 这个 helper 必须能操作编排（要有 socket、要能看到编排目录），
// 权限实际上接近宿主机管理员。因此本文件坚守三条：
//
//  1. **命令固定**：只运行 `compose up` 更新自身服务；纳管分支的固定
//     shell 包装仅准备并清理私有快照，禁止解释调用方传进来的脚本；
//  2. **参数白名单**：服务名、项目名、目录来自容器标签并逐项校验；
//     文件来自同目录的原配置，或来自同项目的私有快照；
//  3. **镜像用当前容器自己的镜像 ID**：不额外拉取，也就不存在
//     "为了自更新而先拉一个陌生镜像"的风险。
//
// 与之对应的取舍：不支持自更新的部署（非 compose、tcp 端点、多配置文件、
// 目录不合规）一律给出**固定中文原因**，绝不猜测着硬做。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"arkade/protocol/envelope"
)

const (
	// selfUpdateHelperPrefix 是一次性容器名前缀。
	//
	// 必须与 helper_reaper 的清理前缀区分开：那个 reaper 清的是
	// 卷/配置读取用的 helper，而本容器要活过我方容器重启，
	// 被提前回收就会让更新半途而废。清理由调用方在重启后负责。
	selfUpdateHelperPrefix = envelope.SelfUpdateHelperPrefix

	// selfUpdateLabel 打在一次性容器上，便于重启后找回并清理。
	selfUpdateLabel = "io.arkade.role.selfupdate"

	// labelSelfComposeProject / labelSelfComposeService 是 compose 写在容器上的
	// 项目元数据。配置路径的两个标签复用 compose_source.go 里的常量。
	labelSelfComposeProject = "com.docker.compose.project"
	labelSelfComposeService = "com.docker.compose.service"

	// defaultSelfComposeFile 已移除：配置文件缺失时不再猜文件名，
	// 而是少传一个 --file 交给 compose 在编排目录里自动发现。

	// selfUpdateMaxLabelLen 限制从标签读到的字符串长度，防御异常值。
	selfUpdateMaxLabelLen = 512

	// selfUpdateRemoveTimeout 是删除残留容器时的独立超时。
	selfUpdateRemoveTimeout = 15 * time.Second
)

// ErrSelfUpdateUnsupported 表示当前部署方式不支持自更新。
var ErrSelfUpdateUnsupported = errors.New("当前部署方式不支持自更新")

// SelfUpdateBlocker 是"这个部署不能自更新"的**结论**，不是故障。
//
// 与 SourceBlockerError 同一套约定：Reason 是我方固定中文文案，
// 绝不把宿主路径拼进去。前端按它展示"为什么不能点"。
type SelfUpdateBlocker struct {
	Reason string
}

func (e *SelfUpdateBlocker) Error() string { return e.Reason }

func selfUpdateBlocked(format string, args ...any) error {
	return &SelfUpdateBlocker{Reason: fmt.Sprintf(format, args...)}
}

// SelfContainerInfo 是"我自己是哪个容器、由哪份编排管理"的探测结果。
type SelfContainerInfo struct {
	ContainerID string
	Name        string
	// ImageID 是当前容器所用镜像的**内容 ID**（helper 直接用它启动）。
	ImageID string
	// ImageRef 是当前容器声明的镜像引用（仅用于展示与日志）。
	ImageRef string

	Project    string
	Service    string
	WorkingDir string
	ConfigFile string
}

// DetachedComposeUp 描述一次"借一次性容器重建自己"的请求。
type DetachedComposeUp struct {
	// ImageID 一次性容器使用的镜像（当前容器自己的镜像 ID）。
	ImageID string
	// Project / WorkingDir / ConfigFile / Service 来自容器标签，已校验。
	Project    string
	WorkingDir string
	ConfigFile string
	Service    string
	// Name 一次性容器名（含 request_id）。残留时会被清理。
	Name string
	// RequestID 写入标签，便于重启后对账。
	RequestID string
	Managed   *envelope.SelfUpdateManagedConfig
}

// SelfUpdateRuntime 是可选能力：更新自己所在容器的镜像。
//
// 与 ComposeSourceRuntime 一样用**独立可选接口**，不改既有 Runtime 接口，
// 因此所有已有 mock 无需改动；不支持的运行时断言失败即可。
type SelfUpdateRuntime interface {
	// SelfContainerInfo 探测自己所在的容器与所属编排。
	// 不可自更新时返回 *SelfUpdateBlocker。
	SelfContainerInfo(ctx context.Context) (SelfContainerInfo, error)
	// RunDetachedComposeUp 创建并启动一次性容器执行重建，返回容器 ID。
	// 本方法**不等待**它结束——等待必然等不到，因为自己会被它杀掉。
	RunDetachedComposeUp(ctx context.Context, req DetachedComposeUp) (string, error)
}

// selfUpdateNamePattern 是服务名 / 项目名白名单。
//
// 之所以自己再校验一遍（引擎那边也会校验）：这两个值会被拼进一条
// 以 root 权限、挂着 docker socket 的容器命令里，多一道白名单是廉价的。
var selfUpdateNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// SelfContainerInfo 定位自己所在的容器，再读它的编排标签。
//
// 定位方式见 detectSelfContainerID 的注释。
func (e *engine) SelfContainerInfo(ctx context.Context) (SelfContainerInfo, error) {
	id, err := e.detectSelfContainerID(ctx)
	if err != nil {
		return SelfContainerInfo{}, err
	}

	var raw struct {
		ID     string `json:"Id"`
		Name   string `json:"Name"`
		Image  string `json:"Image"`
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	if err := e.client.getJSON(ctx, pathContainerPrefix+url.PathEscape(id)+"/json", &raw); err != nil {
		return SelfContainerInfo{}, fmt.Errorf("读取自己所在容器的信息失败: %w", err)
	}
	if raw.ID == "" || raw.Image == "" {
		return SelfContainerInfo{}, errors.New("引擎没有返回自己所在容器的完整信息")
	}

	info := SelfContainerInfo{
		ContainerID: raw.ID,
		Name:        strings.TrimPrefix(raw.Name, "/"),
		ImageID:     raw.Image,
		ImageRef:    trimSelfLabel(raw.Config.Image),
		Project:     trimSelfLabel(raw.Config.Labels[labelSelfComposeProject]),
		Service:     trimSelfLabel(raw.Config.Labels[labelSelfComposeService]),
		WorkingDir:  trimSelfLabel(raw.Config.Labels[labelComposeWorkingDir]),
		ConfigFile:  trimSelfLabel(raw.Config.Labels[labelComposeConfigFiles]),
	}

	if info.Project == "" || info.Service == "" {
		return SelfContainerInfo{}, selfUpdateBlocked(
			"当前容器不是由 compose 启动的（缺少编排标签），无法自更新。" +
				"请改用 docker compose 部署，或手动执行 docker compose pull 后再 up -d")
	}
	if !selfUpdateNamePattern.MatchString(info.Project) {
		return SelfContainerInfo{}, selfUpdateBlocked("当前编排的项目名含不支持的字符，已拒绝自更新")
	}
	if !selfUpdateNamePattern.MatchString(info.Service) {
		return SelfContainerInfo{}, selfUpdateBlocked("当前容器的服务名含不支持的字符，已拒绝自更新")
	}
	if info.WorkingDir == "" {
		return SelfContainerInfo{}, selfUpdateBlocked("当前编排没有记录工作目录，无法定位配置文件")
	}
	if !isSafeHostPath(info.WorkingDir) {
		return SelfContainerInfo{}, selfUpdateBlocked("当前编排的工作目录不是一个安全的绝对路径，已拒绝自更新")
	}
	// 多配置文件时无法确定"哪一份是这份编排"的真实来源，
	// 猜一份去 up 可能覆盖掉另一份的意图。宁可拒绝。
	if strings.Contains(info.ConfigFile, ",") {
		return SelfContainerInfo{}, selfUpdateBlocked("当前编排使用了多个配置文件，暂不支持自更新")
	}
	// 配置文件名缺失时**不去猜**：老版本 compose 可能只写 working_dir，
	// 而目录里到底是 compose.yml / compose.yaml / docker-compose.yml
	// 只有 compose 自己知道。少传一个 --file，让它按 --project-directory
	// 自动发现，比猜一个可能不存在的文件名稳得多（猜错会白失败一次）。
	if info.ConfigFile != "" {
		if !isSafeHostPath(info.ConfigFile) {
			return SelfContainerInfo{}, selfUpdateBlocked("当前编排的配置文件路径不安全，已拒绝自更新")
		}
		// 配置文件必须在工作目录内：否则 helper 挂载了工作目录却读不到配置，
		// 或读到的其实是另一个项目的配置。
		if info.ConfigFile != selfUpdatePrivateCompose && !strings.HasPrefix(info.ConfigFile, strings.TrimSuffix(info.WorkingDir, "/")+"/") {
			return SelfContainerInfo{}, selfUpdateBlocked("当前编排的配置文件不在其工作目录内，已拒绝自更新")
		}
	}
	return info, nil
}

// detectSelfContainerID 依次尝试三条线索定位自己所在的容器。
//
// 为什么需要三条：这三条线索各自都有"在某些环境里拿不到"的情况，
// 而任何一条拿不到都不该让整个功能失效。
//
//  1. hostname —— 容器默认拿容器 ID 前 12 位当 hostname，最省事。
//     但编排里显式写了 `hostname:` 就把它覆盖了，此时这个名字
//     既不是 ID 也不一定是容器名，解析不出来。
//  2. /proc/self/mountinfo —— Docker 必定把 /etc/hostname、/etc/hosts、
//     /etc/resolv.conf 从 `<data-root>/containers/<完整 ID>/` 挂进来，
//     所以挂载来源里就带着自己的完整 ID。
//     这是**唯一与 cgroup 版本无关**的线索，也是本机实测能救回来的那条。
//  3. /proc/self/cgroup —— 只在 cgroup v1 或 `cgroupns=host` 时带 ID。
//     Docker 20.10 起 cgroup v2 默认用**私有** cgroup 命名空间，
//     此时这个文件里只有 `0::/`，一个 ID 都没有——
//     把它当主线索会在绝大多数新环境上直接失败（这正是线上踩到的坑）。
//
// 不用"按镜像名猜"：同一镜像可能跑着多个容器（比如多实例部署），
// 猜错会把**另一个**容器当成自己重建，那是不可接受的误伤。
func (e *engine) detectSelfContainerID(ctx context.Context) (string, error) {
	host := selfHostname()
	if host != "" {
		// 先确认这个 hostname 真的能解析成一个容器，再采用它。
		if id := e.resolveContainerID(ctx, host); id != "" {
			return id, nil
		}
	}
	if id := containerIDFromMountinfo(); id != "" {
		// 自己的挂载命名空间里只可能出现自己的 etc 文件来源，
		// 但拿到 ID 后仍然过一遍引擎，顺便换回规范写法。
		if resolved := e.resolveContainerID(ctx, id); resolved != "" {
			return resolved, nil
		}
	}
	if id := containerIDFromCgroup(); id != "" {
		if resolved := e.resolveContainerID(ctx, id); resolved != "" {
			return resolved, nil
		}
	}
	// 把 hostname 写进原因：失败时这是唯一能远程看到的线索，
	// 否则只能靠猜用户那边的部署形态。
	return "", selfUpdateBlocked(
		"无法确认自己运行在哪个容器里（hostname=%q，mountinfo 与 cgroup 中都没有容器 ID）。"+
			"请确认执行面运行在容器中，或手动执行 docker compose pull 后再 up -d", host)
}

// selfHostname 取本进程看到的 hostname。
//
// 环境变量优先：Docker 总会设置 HOSTNAME，且它与容器的实际 hostname 一致；
// 而 os.Hostname() 在某些精简镜像里可能读不到。
func selfHostname() string {
	if h := strings.TrimSpace(os.Getenv("HOSTNAME")); h != "" {
		return h
	}
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(h)
}

// resolveContainerID 用 ref（完整 ID、ID 前缀或名称）向引擎确认容器存在。
// 取不到就返回空串，由调用方继续试下一条线索。
func (e *engine) resolveContainerID(ctx context.Context, ref string) string {
	var probe struct {
		ID string `json:"Id"`
	}
	if err := e.client.getJSON(ctx, pathContainerPrefix+url.PathEscape(ref)+"/json", &probe); err != nil {
		return ""
	}
	return probe.ID
}

// selfMountinfoPath 是记录挂载来源的文件路径。
//
// 拆成变量而不是写死，是为了让测试能替换它——测试进程自己的
// mountinfo 里通常没有容器 ID，写死就无法构造边界用例。
var selfMountinfoPath = "/proc/self/mountinfo"

// containerDirInMounts 从挂载来源里抠出容器目录 `<data-root>/containers/<64 位 ID>`。
//
// 只认带 `containers/` 前缀的形态，不用裸 64 位十六进制：
// 裸匹配可能撞上卷路径或设备名里的哈希段，把别的容器 ID 当成自己。
var containerDirInMounts = regexp.MustCompile(`containers/([0-9a-f]{64})`)

// containerIDFromMountinfo 从 /proc/self/mountinfo 里取自己的容器 ID。
func containerIDFromMountinfo() string {
	data, err := os.ReadFile(selfMountinfoPath)
	if err != nil {
		return ""
	}
	return firstContainerIDFromMounts(string(data))
}

// firstContainerIDFromMounts 从 mountinfo 文本里取出第一个容器 ID。
//
// 单独拆出来是为了能直接测各种 data-root 形态（默认的 /var/lib/docker、
// 群晖/fnOS 那类自定义路径、以及带 `containers/` 字样的干扰项）。
func firstContainerIDFromMounts(content string) string {
	m := containerDirInMounts.FindStringSubmatch(content)
	if len(m) < 2 {
		return ""
	}
	return m[1]
}

// selfCgroupPaths 是可能含容器 ID 的 cgroup 描述文件。
//
// cgroup v1 记 `/docker/<64 位 ID>`，v2 且 cgroupns=host 时记
// `/system.slice/docker-<ID>.scope`，两种都能用同一条正则捞出来。
// 注意 v2 + 私有 cgroup 命名空间时这里是空的，所以只能当兜底。
var selfCgroupPaths = []string{"/proc/self/cgroup", "/proc/1/cgroup"}

var containerIDPattern = regexp.MustCompile(`[0-9a-f]{64}`)

func containerIDFromCgroup() string {
	for _, p := range selfCgroupPaths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if id := firstContainerID(string(data)); id != "" {
			return id
		}
	}
	return ""
}

// firstContainerID 从 cgroup 文本里取出第一个 64 位容器 ID。
//
// 单独拆出来是为了能直接测：读固定文件路径的版本在测试环境里
// 取到的是**测试进程自己**的 cgroup，没法构造边界用例。
func firstContainerID(content string) string {
	return containerIDPattern.FindString(content)
}

// trimSelfLabel 读取标签值并做长度与空白处理。
func trimSelfLabel(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > selfUpdateMaxLabelLen {
		return ""
	}
	return v
}

// isSafeHostPath 判断一个宿主路径是否允许交给一次性容器挂载。
//
// 规则与 compose_source.go 的读取路径一致（绝对、已规范化、非根、
// 非系统目录），但这里**允许写**：一次性容器要执行 compose，
// 而 compose 会在编排目录里读写临时文件。
func isSafeHostPath(p string) bool {
	if p == "" || !path.IsAbs(p) {
		return false
	}
	clean := path.Clean(p)
	if clean == "/" || clean != p {
		return false
	}
	for _, bad := range []string{"/proc", "/sys", "/dev", "/etc", "/run", "/var/run"} {
		if clean == bad || strings.HasPrefix(clean, bad+"/") {
			return false
		}
	}
	return true
}

// dockerSocketPath 把 unix 端点还原成宿主路径。
//
// 只有 unix 端点才可能自更新：tcp 端点意味着引擎在另一台机器或走加密通道，
// 那种形态下"挂一个 socket 进去"没有意义，也无法在 helper 里访问。
func (e *engine) dockerSocketPath() (string, error) {
	ep := strings.TrimSpace(e.endpoint)
	for _, prefix := range []string{"unix://", "unix:"} {
		if strings.HasPrefix(ep, prefix) {
			p := strings.TrimPrefix(ep, prefix)
			if !path.IsAbs(p) {
				return "", selfUpdateBlocked("当前引擎的 socket 路径不规范，已拒绝自更新")
			}
			return p, nil
		}
	}
	return "", selfUpdateBlocked(
		"当前引擎不是通过本机 socket 连接的，不支持自更新。" +
			"请手动执行 docker compose pull 后再 up -d")
}

// RunDetachedComposeUp 创建并启动一次性容器。
//
// 它执行的是**唯一一条**命令：
//
//	docker compose --project-name <p> --project-directory <dir> --file <f> \
//	    up -d --no-deps --pull always <service>
//
// `--pull always` 是关键：先拉镜像，拉不到就失败退出，**不会**动旧容器；
// 拉到且内容有变化才重建。也就实现了"拉取失败则一切照旧"。
//
// 不加 `--force-recreate`：若镜像本来就没变化（例如编排里固定了旧 tag），
// 强行重建只会造成一次无意义的停机，而版本号不会变。
//
// `--project-name` 必须显式传：否则 compose 会按目录名重新推算项目名，
// 算错时会**新建一套**容器，与旧容器抢端口，比不更新更糟。
func (e *engine) RunDetachedComposeUp(ctx context.Context, req DetachedComposeUp) (string, error) {
	if !selfUpdateNamePattern.MatchString(req.Service) || !selfUpdateNamePattern.MatchString(req.Project) {
		return "", selfUpdateBlocked("服务名或项目名不合法，已拒绝自更新")
	}
	if !isSafeHostPath(req.WorkingDir) {
		return "", selfUpdateBlocked("编排路径不合法，已拒绝自更新")
	}
	// ConfigFile 允许为空（交给 compose 自动发现），但一旦非空就必须安全。
	if req.ConfigFile != "" && !isSafeHostPath(req.ConfigFile) {
		return "", selfUpdateBlocked("编排路径不合法，已拒绝自更新")
	}
	if req.ImageID == "" || !strings.HasPrefix(req.Name, selfUpdateHelperPrefix) {
		return "", errors.New("自更新参数不完整")
	}
	socket, err := e.dockerSocketPath()
	if err != nil {
		return "", err
	}

	if err := validateManagedSelfUpdate(req); err != nil {
		return "", err
	}
	if id, err := e.checkSelfUpdateHelpers(ctx, req.Name); err != nil || id != "" {
		return id, err
	}
	body := helperContainerConfig{
		Image: req.ImageID,
		// 直接用 docker 本体作为入口：镜像里本来就有 CLI 与 compose 插件，
		// 不需要注入二进制，也就不需要"读我方可执行文件"这一步。
		Entrypoint: []string{"docker"},
		Cmd:        composeUpArgs(req),
		// root：只有 root 能打开宿主 socket。
		User: "0:0",
		Env: []string{
			"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"DOCKER_HOST=unix:///var/run/docker.sock",
		},
		// 工作目录与宿主同路径：compose 里的相对挂载、env_file
		// 都按这个目录解析，换成别的路径就会解析错。
		WorkingDir: req.WorkingDir,
		Labels: map[string]string{
			selfUpdateLabel: req.RequestID,
		},
		HostConfig: helperHostConfig{
			ReadOnlyRootfs: false,
			Privileged:     false,
			// AutoRemove=false：失败时要能读日志，清理由调用方在重启后负责。
			AutoRemove:  false,
			NetworkMode: "default",
			CapDrop:     []string{"ALL"},
			SecurityOpt: []string{"no-new-privileges"},
			Tmpfs:       map[string]string{"/tmp": "rw,exec,nosuid,size=64m"},
			Memory:      256 << 20,
			NanoCpus:    1_000_000_000,
			// 挂两样：宿主 socket（操作引擎）+ 编排目录（读 compose 配置）。
			// 编排目录挂在**同一路径**上，否则 compose 里的相对路径会解析错。
			Mounts: []helperMount{
				{
					Type:   "bind",
					Source: socket,
					Target: "/var/run/docker.sock",
				},
				{
					Type:   "bind",
					Source: req.WorkingDir,
					Target: req.WorkingDir,
				},
			},
		},
		Networking: helperNetworkConfig{NetworkMode: "default"},
	}

	if req.Managed != nil {
		body.Entrypoint = []string{"/bin/sh", "-c", selfUpdateManagedScript, "arkade-selfupdate"}
		body.HostConfig.Mounts[1].ReadOnly = true
		body.Env = append(body.Env, "COMPOSE_DISABLE_ENV_FILE=1", "COMPOSE_REMOVE_ORPHANS=0", "COMPOSE_MENU=0")
	} else {
		// 非纳管路径要读**宿主上**的 compose 文件与 .env，它们常是 0600/0700。
		// helper 是 User:0:0 + CapDrop ALL，已经连 DAC_OVERRIDE 一起丢了，
		// 于是容器内的 root 也受文件权限位约束——直接读会 permission denied
		// （和 helperSpec.readSearch 描述的是同一个坑）。
		//
		// 正确的做法不是让用户放宽自己的文件权限，而是只加回"读与目录遍历"：
		// DAC_READ_SEARCH 不授予写、不改变属主，也扩不大挂载范围。
		//
		// 纳管路径读的是自己层里的快照，没有这个需求，因此保持不加。
		body.HostConfig.CapAdd = []string{"DAC_READ_SEARCH"}
	}
	raw, merr := json.Marshal(body)
	if merr != nil {
		return "", fmt.Errorf("序列化自更新容器配置失败: %w", merr)
	}

	id, cerr := e.createContainer(ctx, req.Name, raw)
	if cerr != nil {
		return "", fmt.Errorf("创建自更新容器失败: %w", cerr)
	}
	removeFailed := func(force bool) {
		delCtx, cancel := context.WithTimeout(context.Background(), selfUpdateRemoveTimeout)
		defer cancel()
		query := "?v=1"
		if force {
			query += "&force=1"
		}
		_ = e.client.delete(delCtx, pathContainerPrefix+url.PathEscape(id)+query)
	}
	if req.Managed != nil {
		if err := e.putManagedSelfUpdateArchive(ctx, id, req.Managed); err != nil {
			removeFailed(true) // 尚未尝试启动，可确定不会中断 Compose。
			return "", errors.New("无法将纳管配置交付给自更新容器")
		}
	}
	if err := e.client.post(ctx, pathContainerPrefix+url.PathEscape(id)+actionStart); err != nil {
		// 若启动已送达但响应丢失，Docker 会拒绝删除运行中的容器。
		removeFailed(false)
		return "", fmt.Errorf("启动自更新容器失败: %w", err)
	}
	return id, nil
}

// composeUpArgs 拼出 helper 容器里要执行的那条 compose 命令。
//
// 固定 argv、不经过 shell：项目名 / 服务名 / 路径都只作为**独立的参数项**
// 传入，不会被拼进命令行里解释，因此不存在注入。
//
// `--file` 只在拿到确定的配置文件时才传。缺失时交给 compose 在
// `--project-directory` 下自己发现（compose.yml / compose.yaml /
// docker-compose.yml 都认），比我们猜一个文件名更可靠。
func composeUpArgs(req DetachedComposeUp) []string {
	args := []string{
		"compose",
		"--project-name", req.Project,
		"--project-directory", req.WorkingDir,
	}
	if req.Managed != nil {
		args = append(args, "--file", selfUpdatePrivateCompose, "--env-file", selfUpdatePrivateEnv)
		return append(args, "up", "-d", "--no-deps", "--no-build", "--pull", "always", req.Service)
	}
	if req.ConfigFile != "" {
		args = append(args, "--file", req.ConfigFile)
	}
	return append(args, "up", "-d", "--no-deps", "--pull", "always", req.Service)
}

// checkSelfUpdateHelpers 先阻断尚未完成的助手，再回收过期诊断容器。
// 不强制删除容器：从列表读取后它仍可能改变状态。
func (e *engine) checkSelfUpdateHelpers(ctx context.Context, name string) (string, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {selfUpdateLabel}})
	var list []struct {
		ID      string
		Names   []string
		State   string
		Created int64
	}
	query := "/containers/json?all=1&filters=" + url.QueryEscape(string(filters))
	if err := e.client.getJSON(ctx, query, &list); err != nil {
		return "", fmt.Errorf("确认是否有正在执行的自更新失败: %w", err)
	}
	for _, c := range list {
		// 进程异常终止可能遗留未启动的快照。超过窗口才回收，
		// 且不加 force，以免把已经开始工作的助手杀掉。
		stale := c.ID != "" && c.Created > 0 && time.Since(time.Unix(c.Created, 0)) > 24*time.Hour
		if stale && c.State == "created" && e.client.delete(ctx, pathContainerPrefix+url.PathEscape(c.ID)+"?v=1") == nil {
			continue
		}
		for _, n := range c.Names {
			// created 可能是未完成上传或异常退出的残留，不能报已受理。
			if strings.TrimPrefix(n, "/") == name && c.State != "created" {
				return c.ID, nil
			}
		}
		if c.State != "exited" && c.State != "dead" {
			return "", selfUpdateBlocked("已有自更新助手尚未结束，请检查后再试")
		}
		if stale {
			_ = e.client.delete(ctx, pathContainerPrefix+url.PathEscape(c.ID)+"?v=1")
		}
	}
	return "", nil
}

// SelfUpdateHelperName 保留 runtime 调用方的旧入口，协议层共享算法。
func SelfUpdateHelperName(requestID string) string {
	return envelope.SelfUpdateHelperName(requestID)
}
