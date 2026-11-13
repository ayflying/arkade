package runtime

// compose_source.go：从引擎标签反查外部编排的工作目录，只读读回 compose 配置。
//
// ============================ 信任边界（重要） ============================
//
// 配置路径来自容器标签（com.docker.compose.project.config_files / .working_dir）。
// **标签不是权限来源**：能写标签的人（宿主机管理员、有 docker 权限者）
// 本来就能做任何事。因此本文件不声称"标签证明这个路径安全"，
// 它只保证一件事：
//
//	**可读范围不会超出"该编排项目的工作目录"这一块，且只读。**
//
// 约束手段（从弱到强）：
//  1. 只接受绝对、已规范化、非 root 的工作目录；
//  2. 拒绝系统目录（/proc /sys /dev /etc /run /var/run）与
//     明确禁止的顶层目录（/root /home /opt /var /tmp /srv）；
//  3. **只 bind 工作目录本身**挂到固定 /source，不挂 config 的父目录、
//     不挂 /、不挂 rootfs；
//  4. 容器内再用 no-follow + 同 fd fstat 打开相对路径，拒 symlink/特殊文件/多硬链接；
//  5. NetworkMode none、CapDrop ALL、no-new-privileges、非特权。

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
)

const (
	// 容器标签：compose v2 写在容器上的项目元数据。
	labelComposeConfigFiles = "com.docker.compose.project.config_files"
	labelComposeWorkingDir  = "com.docker.compose.project.working_dir"

	// maxComposeSourceBytes 是单份 compose 文件上限，对齐 ScanImportSafety。
	maxComposeSourceBytes = 1 << 20

	// maxWorkingDirLen 限制工作目录长度（防御异常长的标签值）。
	maxWorkingDirLen = 512
)

var (
	// ErrComposeSourceUnavailable 表示没有可用的来源信息。
	// SourceBlockerError.Reason 提供详细原因，哨兵错误用于分类判断。
	ErrComposeSourceUnavailable = errors.New("该项目没有可用的配置来源信息")
	// ErrComposeSourceUnsafePath 表示标签里给出的路径被安全规则拒绝。
	ErrComposeSourceUnsafePath = errors.New("该项目声明的配置路径未被允许读取")
)

// ComposeSourceRuntime 是可选能力：只读读回外部编排的配置文件。
//
// 与 VolumeFileRuntime 一样使用独立能力接口，使不支持外部配置读取的运行时
// 无需暴露该方法。
type ComposeSourceRuntime interface {
	RunComposeSource(ctx context.Context, name string) (string, error)
}

// ComposeSourceRawRuntime 是"原样读取"能力（未接管项目的查看/编辑）。
//
// 与自动接管所需的普通读取能力分离，避免后台扫描隐式获得增强读取权限。
type ComposeSourceRawRuntime interface {
	// RunComposeSourceRaw 与 RunComposeSource 读取同样的内容，
	// 但会给 helper 加 DAC_READ_SEARCH，以读取 0600/0700 的配置。
	//
	// 只在用户显式查看/编辑某个项目时使用；自动接管永远走非 raw 路径。
	RunComposeSourceRaw(ctx context.Context, name string) (string, error)
}

// SourceBlockerError 表示"这个项目现在不能被接管"，而不是"这次调用失败了"。
//
// ============================ 为什么要区分这两件事 ============================
//
// 多配置文件、标签不一致、工作目录是系统目录、配置是符号链接……
// 这些都是**关于这个项目的结论**，用户需要看到具体原因才能去改。
// 如果把它们当成普通 error 往上传，handler 只能兜底成
// "读取配置失败"，Hub 再统一显示"请检查版本/镜像/权限"——
// 用户按提示查一圈，发现都不对，真正的原因却从头到尾没出现过。
//
// 因此约定：
//   - 预期内的"不能接管" → 返回 SourceBlockerError（Reason 是固定中文）；
//   - 真正的故障（引擎不可达、超时、并发忙、镜像缺失）→ 返回普通 error。
//
// Reason 必须是**我方固定文案**，绝不能把 os.PathError 之类的原始错误
// 拼进去——那会带上宿主路径。
type SourceBlockerError struct {
	// Reason 是给用户看的固定中文原因。
	Reason string
}

func (e *SourceBlockerError) Error() string { return e.Reason }

// sourceBlocked 构造一条阻塞结论。
func sourceBlocked(format string, args ...any) error {
	return &SourceBlockerError{Reason: fmt.Sprintf(format, args...)}
}

// composeSourceDoc 是 helper 返回的读取结果。
type composeSourceDoc struct {
	// ContentBase64 是文件内容（base64）。
	ContentBase64 string `json:"content_base64,omitempty"`
	// Error 是 helper 侧给出的固定中文原因（不含路径）。
	Error string `json:"error,omitempty"`
}

// composeSourceRequest 是发给 helper 的请求。
type composeSourceRequest struct {
	// ConfigFile 是**相对于 /source 的路径**，不是宿主绝对路径。
	ConfigFile string `json:"config_file"`
}

// RunComposeSource 反查项目配置并读回内容。
//
// 返回的是原始 compose 内容与哈希；安全扫描由上层（handler）再做一次过滤，
// 这样 runtime 层不必知道 compose 的语义规则。
//
// **capability 与 raw 路径一致**（都加 DAC_READ_SEARCH）：
// 用户的明确要求是"容器可管理 + 读取统一"——自动接管如果读不到 0600 的配置，
// 就会出现"手动能看、自动接管却说权限不足"的分裂行为，那更难解释也更难用。
// 挂载范围、no-follow、nlink 校验、只读挂载全部不变，因此可读面没有扩大。
//
// 两条路径的差别**只在 handler 层**：raw 决定有阻塞时是否仍然回传内容。
func (e *engine) RunComposeSource(ctx context.Context, name string) (string, error) {
	return e.runComposeSource(ctx, name, true)
}

// RunComposeSourceRaw 是显式查看/编辑路径。
//
// 与 RunComposeSource 读取完全相同（含 capability）；
// 区别在 handler：raw 模式下即使有导入阻塞也会回传原文，
// 便于用户查看/编辑含 env_file、build 的编排。
func (e *engine) RunComposeSourceRaw(ctx context.Context, name string) (string, error) {
	return e.runComposeSource(ctx, name, true)
}

// runComposeSource 是两条公开入口的共同实现。
func (e *engine) runComposeSource(ctx context.Context, name string, readSearch bool) (content string, err error) {
	if strings.TrimSpace(name) == "" {
		return "", errors.New("编排项目名不能为空")
	}

	// 总时限在**入口**就建立：resolve 也要查引擎容器列表，
	// 若只由 runHelper 内部计时，前面的 resolve 不受这套预算约束。
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()

	workDir, configRel, err := e.resolveComposeSource(ctx, name)
	if err != nil {
		return "", err
	}

	reqJSON, err := json.Marshal(composeSourceRequest{ConfigFile: configRel})
	if err != nil {
		return "", fmt.Errorf("序列化请求失败: %w", err)
	}

	raw, err := e.runHelper(ctx, helperSpec{
		// 隐藏子命令：固定从 /source 读，不接受挂载点参数。
		cmd: []string{"compose-source-helper"},
		// ============================ helper 必须带上 Agent 的挂载根 ============================
		// Agent 是容器时，编排放置的目录是**宿主路径**；helper 同样是新容器，
		// 只挂 Agent 自己的容器内路径（/arkade/external/...）是挂不到的——
		// 那里只有宿主机上恰好存在同名路径时才看得到东西。
		//
		// 所以：宿主机目录按**宿主路径**挂进来；Agent 内存路径额外挂一份。
		// 真正要读的那份在宿主挂载里，容器内路径只是兜底与排障便利。
		mounts:       e.composeSourceMounts(workDir),
		request:      reqJSON,
		responsePath: helperSourceResponsePath,
		// 只有 raw 路径会加 DAC_READ_SEARCH。
		readSearch: readSearch,
	})
	if err != nil {
		return "", err
	}

	var doc composeSourceDoc
	if jerr := json.Unmarshal(raw, &doc); jerr != nil {
		return "", errors.New("编排来源助手返回了无法解析的结果")
	}
	if doc.Error != "" {
		// helper 给出的原因是我方固定文案（不含路径），属于"项目当前不能接管"，
		// 而不是本次调用故障——原样作为 blocker 上抛。
		return "", &SourceBlockerError{Reason: doc.Error}
	}
	data, derr := base64.StdEncoding.DecodeString(doc.ContentBase64)
	if derr != nil {
		return "", errors.New("编排来源助手返回了无效内容")
	}
	if len(data) > maxComposeSourceBytes {
		return "", errors.New("编排文件超过 1MB 上限")
	}
	return string(data), nil
}

// readonlyBind 构造一个只读 bind 挂载，且**禁止** daemon 创建缺失目录。
func readonlyBind(src, dst string) helperMount {
	return helperMount{
		Type:        "bind",
		Source:      src,
		Target:      dst,
		ReadOnly:    true,
		BindOptions: &helperBindOptions{CreateMountpoint: false},
	}
}

// composeSourceMounts 构造 compose-source-helper 的挂载列表。
//
// 容器化部署（配了宿主根目录）时挂两份：
//   - 宿主路径 → /source：这是真正被读取的那份，helper 是新容器，
//     只有用宿主路径挂才能看到宿主目录；
//   - 容器内路径 → 容器内路径：Agent 内存路径本身来自宿主目录的只读挂载，
//     挂一份等价实现，方便排障时对照。
//
// 原生部署（没有配宿主根目录）时只有第二份，行为与以前完全一致。
func (e *engine) composeSourceMounts(workDir string) []helperMount {
	mounts := make([]helperMount, 0, 2)
	if e != nil && e.hostRoot != "" {
		if hostDir, ok := e.containerPathToHost(workDir); ok {
			mounts = append(mounts, readonlyBind(hostDir, sourceMountPoint))
		}
	}
	return append(mounts, readonlyBind(workDir, sourceMountPoint))
}

// containerPathToHost 是 hostPath 的逆运算：把容器内路径还回宿主路径。
//
// 只用于"再挂一份给 helper"这种等价补偿，路径不合规时返回 ok=false，
// 由调用方跳过这一份，不影响主挂载。
func (e *engine) containerPathToHost(p string) (string, bool) {
	if e == nil || e.hostRoot == "" {
		return "", false
	}
	mount := e.hostMount
	if mount == "" {
		mount = DefaultHostMount
	}
	if !strings.HasPrefix(p, mount+"/") {
		return "", false
	}
	return strings.TrimSuffix(e.hostRoot, "/") + p[len(mount):], true
}

// resolveComposeSource 从容器标签反查工作目录与相对配置文件路径。
//
// 规则（Lead 指定）：
//   - 同项目所有非 oneoff 容器的 config_files / working_dir 必须**一致**，
//     不一致就拒绝（说明这个项目本身状态矛盾，读哪一份都可能错）；
//   - 只支持**单配置文件**，多文件给明确原因；
//   - 工作目录必须绝对、已规范化、非 root T、且不在禁止的顶层目录；
//   - 配置文件必须在该工作目录**之内**（相对路径不含 ..）。
func (e *engine) resolveComposeSource(ctx context.Context, name string) (workDir, configRel string, err error) {
	labels, err := e.collectProjectLabels(ctx, name)
	if err != nil {
		return "", "", err
	}
	if len(labels) == 0 {
		return "", "", sourceBlocked("引擎里没有名为 %q 的编排项目，找不到可读取的配置来源", name)
	}

	wd := labels[0][labelComposeWorkingDir]
	cf := labels[0][labelComposeConfigFiles]
	// 所有容器的这两个标签必须一致。
	for _, l := range labels[1:] {
		if l[labelComposeWorkingDir] != wd || l[labelComposeConfigFiles] != cf {
			return "", "", sourceBlocked("该项目各容器声明的配置来源标签不一致，无法确定该读哪一份配置")
		}
	}
	if strings.TrimSpace(wd) == "" {
		return "", "", sourceBlocked("该项目没有声明工作目录标签，无法定位配置所在目录")
	}
	if strings.TrimSpace(cf) == "" {
		return "", "", sourceBlocked("该项目没有声明配置文件标签，无法定位配置文件")
	}

	cleanWD, werr := validateWorkingDir(wd)
	if werr != nil {
		return "", "", werr
	}

	// config_files 是逗号分隔的列表。路径里含逗号会被切错，
	// 但那只导致"读不到 → 明确报错"，不会读错文件。
	var files []string
	for _, part := range strings.Split(cf, ",") {
		if p := strings.TrimSpace(part); p != "" {
			files = append(files, p)
		}
	}
	if len(files) == 0 {
		return "", "", sourceBlocked("该项目没有声明任何配置文件")
	}
	if len(files) > 1 {
		return "", "", sourceBlocked(
			"该项目使用了多个配置文件（%d 个），首版仅支持单配置文件的项目", len(files))
	}

	rel, rerr := relativeConfigPath(cleanWD, files[0])
	if rerr != nil {
		return "", "", rerr
	}

	// ============================ 容器化部署的路径换算 ============================
	// 安全校验全部作用在**标签里的宿主机路径**上（那才是"这个项目声明了什么"），
	// 校验通过后才换算成本进程容器内可见的路径去挂载。
	//
	// 顺序很重要：先换算再校验会把容器内前缀（/arkade/external）当成工作目录，
	// 让它看起来总是"合法"，等于跳过了对真实来源的检查。
	mountDir, ok := e.hostPath(cleanWD)
	if !ok {
		// 宿主目录没有挂进 Agent：如实报告"读不到"，
		// 不猜一个相近路径去试——那会把别的目录当成这个项目的配置。
		return "", "", sourceBlocked(
			"该项目位于宿主机目录 %s，但该目录没有挂载进 Agent 容器，因此读不到配置；"+
				"如需在 Arkade 里查看或编辑，请在部署时把该目录只读挂给 Agent", cleanWD)
	}
	return mountDir, rel, nil
}

// collectProjectLabels 取指定项目所有非 oneoff 容器的标签。
func (e *engine) collectProjectLabels(ctx context.Context, name string) ([]map[string]string, error) {
	containers, err := e.List(ctx, true)
	if err != nil {
		return nil, err
	}
	var out []map[string]string
	for _, c := range containers {
		if strings.TrimSpace(c.Labels["com.docker.compose.project"]) != name {
			continue
		}
		// 排除 docker compose run 的一次性容器：它们可能是临时目录。
		if strings.EqualFold(strings.TrimSpace(c.Labels["com.docker.compose.oneoff"]), "true") {
			continue
		}
		out = append(out, c.Labels)
	}
	return out, nil
}

// forbiddenTopDirs 是明确禁止作为工作目录的顶层目录。
//
// 这些目录下一旦被挂进 helper，就等于把宿主机的大片内容变成可读：
// 家目录可能含私钥，/opt /var /srv 常放其它应用数据，/tmp 是任意可写。
// 允许它们的**子目录**（项目通常就在 /opt/x 或 /srv/y 下），
// 但目录本身不行——那样整个 /opt 就都进去了。
var forbiddenTopDirs = map[string]struct{}{
	"/root":               {},
	"/home":               {},
	"/opt":                {},
	"/var":                {},
	"/tmp":                {},
	"/srv":                {},
	"/proc":               {},
	"/sys":                {},
	"/dev":                {},
	"/etc":                {},
	"/run":                {},
	"/var/run":            {},
	"/boot":               {},
	"/usr":                {},
	"/bin":                {},
	"/sbin":               {},
	"/lib":                {},
	"/lib64":              {},
	"/mnt":                {},
	"/media":              {},
	"/var/lib/docker":     {},
	"/var/lib/containers": {},
}

// validateWorkingDir 校验工作目录。
func validateWorkingDir(p string) (string, error) {
	if p == "" || len(p) > maxWorkingDirLen {
		return "", sourceBlocked("该项目声明的工作目录长度异常")
	}
	// NUL 与反斜杠：会被不同组件解释成不同路径。
	if strings.ContainsAny(p, "\x00\\") {
		return "", sourceBlocked("该项目声明的工作目录包含非法字符")
	}
	if !path.IsAbs(p) {
		return "", sourceBlocked("该项目声明的工作目录不是绝对路径，无法安全读取")
	}
	clean := path.Clean(p)
	if clean != p {
		// 未规范化说明来源可疑（含 .. 或 //）；拒绝而不是替它规范化。
		return "", sourceBlocked("该项目声明的工作目录不是规范路径，为保证只读范围可控已拒绝")
	}
	if clean == "/" {
		return "", sourceBlocked("该项目以文件系统根目录为工作目录，挂载会暴露整台机器，已拒绝")
	}
	if _, bad := forbiddenTopDirs[clean]; bad {
		return "", sourceBlocked(
			"该项目的工作目录是系统或公共顶层目录，只读挂载它会暴露过多内容；请把编排放到独立子目录下再接管")
	}
	return clean, nil
}

// relativeConfigPath 把配置文件的绝对路径换算成相对工作目录的路径。
func relativeConfigPath(workDir, configFile string) (string, error) {
	if strings.ContainsAny(configFile, "\x00\\") {
		return "", sourceBlocked("配置文件路径包含非法字符，已拒绝")
	}
	if !path.IsAbs(configFile) {
		return "", sourceBlocked("配置文件路径不是绝对路径，无法定位")
	}
	clean := path.Clean(configFile)
	if clean != configFile {
		return "", sourceBlocked("配置文件路径不是规范路径，已拒绝")
	}
	// 必须严格位于工作目录之内（不是工作目录本身，那是个目录）。
	if !strings.HasPrefix(clean, workDir+"/") {
		return "", sourceBlocked("配置文件不在该项目的工作目录之内，超出可读取范围，已拒绝")
	}
	rel := clean[len(workDir)+1:]
	if rel == "" || rel == "." {
		return "", sourceBlocked("配置文件路径无效")
	}
	return rel, nil
}
