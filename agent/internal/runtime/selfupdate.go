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
//  1. **命令写死**：只可能是 `compose up -d --no-deps --pull always <service>`，
//     不存在任何"把调用方传进来的命令拿去执行"的代码路径；
//  2. **参数白名单**：服务名、项目名、目录、配置文件全部来自容器标签，
//     且逐项校验（名字走严格正则，目录必须绝对且非根，
//     配置文件必须在编排目录之内）；
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
)

const (
	// selfUpdateHelperPrefix 是一次性容器名前缀。
	//
	// 必须与 helper_reaper 的清理前缀区分开：那个 reaper 清的是
	// 卷/配置读取用的 helper，而本容器要活过我方容器重启，
	// 被提前回收就会让更新半途而废。清理由调用方在重启后负责。
	selfUpdateHelperPrefix = "arkade-selfupdate-"

	// selfUpdateLabel 打在一次性容器上，便于重启后找回并清理。
	selfUpdateLabel = "io.arkade.role.selfupdate"

	// labelSelfComposeProject / labelSelfComposeService 是 compose 写在容器上的
	// 项目元数据。配置路径的两个标签复用 compose_source.go 里的常量。
	labelSelfComposeProject = "com.docker.compose.project"
	labelSelfComposeService = "com.docker.compose.service"

	// defaultSelfComposeFile 是标签缺失时退化的配置文件名。
	defaultSelfComposeFile = "compose.yml"

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
	// CleanupSelfUpdateHelpers 删除残留的一次性容器，keepID 非空时保留它。
	CleanupSelfUpdateHelpers(ctx context.Context, keepID string) (int, error)
}

// selfUpdateNamePattern 是服务名 / 项目名白名单。
//
// 之所以自己再校验一遍（引擎那边也会校验）：这两个值会被拼进一条
// 以 root 权限、挂着 docker socket 的容器命令里，多一道白名单是廉价的。
var selfUpdateNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// SelfContainerInfo 用三步定位自己：
//
//  1. hostname —— 容器默认把 hostname 设成容器 ID 前 12 位，一步命中；
//  2. /proc/self/cgroup —— hostname 被显式覆盖时，cgroup 路径里仍带完整 ID；
//  3. 都不行 → 给出固定中文原因。
//
// 不用"按镜像名猜"：同一镜像可能跑着多个容器（比如多实例部署），
// 猜错会把**另一个**容器当成自己重建，那是不可接受的误伤。
func (e *engine) SelfContainerInfo(ctx context.Context) (SelfContainerInfo, error) {
	id, err := e.detectSelfContainerID(ctx)
	if err != nil {
		return SelfContainerInfo{}, err
	}

	var raw struct {
		ID    string `json:"Id"`
		Name  string `json:"Name"`
		Image string `json:"Image"`
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
	if info.ConfigFile == "" {
		// 老版本 compose 只写 working_dir。退化为目录下的约定文件名。
		info.ConfigFile = path.Join(info.WorkingDir, defaultSelfComposeFile)
	}
	if !isSafeHostPath(info.ConfigFile) {
		return SelfContainerInfo{}, selfUpdateBlocked("当前编排的配置文件路径不安全，已拒绝自更新")
	}
	// 配置文件必须在工作目录内：否则 helper 挂载了工作目录却读不到配置，
	// 或读到的其实是另一个项目的配置。
	if !strings.HasPrefix(info.ConfigFile, strings.TrimSuffix(info.WorkingDir, "/")+"/") {
		return SelfContainerInfo{}, selfUpdateBlocked("当前编排的配置文件不在其工作目录内，已拒绝自更新")
	}
	return info, nil
}

// detectSelfContainerID 依次尝试 hostname 与 cgroup。
func (e *engine) detectSelfContainerID(ctx context.Context) (string, error) {
	host := strings.TrimSpace(os.Getenv("HOSTNAME"))
	if host == "" {
		if h, err := os.Hostname(); err == nil {
			host = strings.TrimSpace(h)
		}
	}
	if host != "" {
		// 先确认这个 hostname 真的对应一个容器，再采用它。
		var probe struct {
			ID string `json:"Id"`
		}
		if err := e.client.getJSON(ctx, pathContainerPrefix+url.PathEscape(host)+"/json", &probe); err == nil && probe.ID != "" {
			return probe.ID, nil
		}
	}
	if id := containerIDFromCgroup(); id != "" {
		return id, nil
	}
	return "", selfUpdateBlocked(
		"无法确认自己运行在哪个容器里（hostname 与 cgroup 都没有容器 ID）。" +
			"请确认执行面运行在容器中，或手动执行 docker compose pull 后再 up -d")
}

// selfCgroupPaths 是可能含容器 ID 的 cgroup 描述文件。
//
// cgroup v1 记 `/docker/<64 位 ID>`，v2 记 `/system.slice/docker-<ID>.scope`，
// 两种都能用同一条正则捞出来。
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
	if !isSafeHostPath(req.WorkingDir) || !isSafeHostPath(req.ConfigFile) {
		return "", selfUpdateBlocked("编排路径不合法，已拒绝自更新")
	}
	if req.ImageID == "" || !strings.HasPrefix(req.Name, selfUpdateHelperPrefix) {
		return "", errors.New("自更新参数不完整")
	}
	socket, err := e.dockerSocketPath()
	if err != nil {
		return "", err
	}

	body := helperContainerConfig{
		Image: req.ImageID,
		// 直接用 docker 本体作为入口：镜像里本来就有 CLI 与 compose 插件，
		// 不需要注入二进制，也就不需要"读我方可执行文件"这一步。
		Entrypoint: []string{"docker"},
		Cmd: []string{
			"compose",
			"--project-name", req.Project,
			"--project-directory", req.WorkingDir,
			"--file", req.ConfigFile,
			"up", "-d", "--no-deps", "--pull", "always", req.Service,
		},
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

	raw, merr := json.Marshal(body)
	if merr != nil {
		return "", fmt.Errorf("序列化自更新容器配置失败: %w", merr)
	}

	id, cerr := e.createContainer(ctx, req.Name, raw)
	if cerr != nil {
		return "", fmt.Errorf("创建自更新容器失败: %w", cerr)
	}
	// 启动失败时把容器删掉，不留一个躺在那里等人排查的半成品。
	if serr := e.client.post(ctx, pathContainerPrefix+url.PathEscape(id)+actionStart); serr != nil {
		delCtx, cancel := context.WithTimeout(context.Background(), selfUpdateRemoveTimeout)
		defer cancel()
		_ = e.client.delete(delCtx, pathContainerPrefix+url.PathEscape(id)+"?force=1&v=0")
		return "", fmt.Errorf("启动自更新容器失败: %w", serr)
	}
	return id, nil
}

// CleanupSelfUpdateHelpers 删除残留的一次性自更新容器。
//
// 何时会残留：更新成功但新进程来不及清理；或更新中途失败。
// 只在**发起新的自更新之前**调用，不做周期性扫描——
// 周期扫描可能在重建进行中把正在干活的容器删掉。
//
// keepID 非空时保留它（自身即当前进行中的那个）。
func (e *engine) CleanupSelfUpdateHelpers(ctx context.Context, keepID string) (int, error) {
	filters, _ := json.Marshal(map[string][]string{"label": {selfUpdateLabel}})
	var list []struct {
		ID string `json:"Id"`
	}
	query := "/containers/json?all=1&filters=" + url.QueryEscape(string(filters))
	if err := e.client.getJSON(ctx, query, &list); err != nil {
		return 0, fmt.Errorf("列出残留的自更新容器失败: %w", err)
	}
	removed := 0
	for _, c := range list {
		if c.ID == "" || c.ID == keepID {
			continue
		}
		if err := e.client.delete(ctx, pathContainerPrefix+url.PathEscape(c.ID)+"?force=1&v=0"); err != nil {
			continue
		}
		removed++
	}
	return removed, nil
}

// SelfUpdateHelperName 生成一次性容器名。
//
// 名字只能含 [a-zA-Z0-9][a-zA-Z0-9_.-]，因此把 request_id 里的非法字符
// 统一折成 "-"，并截断到安全长度。名字同时是幂等键的可见部分。
func SelfUpdateHelperName(requestID string) string {
	var b strings.Builder
	for _, r := range requestID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-_")
	if s == "" {
		s = "manual"
	}
	if len(s) > 40 {
		s = s[:40]
	}
	return selfUpdateHelperPrefix + s
}
