package runtime

// container_recreate.go 实现"按原配置重建容器"。
//
// ============================ 为什么需要这个能力 ============================
// Docker 无法原地替换运行中容器的镜像：镜像字段在容器创建时就固化了。
// 想让新镜像生效，唯一的手段是**删掉旧容器、用同样的配置建一个新容器**。
// 这就是「重建」，也是 Portainer 里那个「Recreate + 重新拉取镜像」的动作。
// restart 做不到：它保留容器对象，镜像还是旧的。
//
// ============================ 顺序是安全性的全部 ============================
// 直觉写法是"先删旧的再建新的"，但那样一旦创建失败，容器就彻底没了。
// 这里采用"改名保住退路"的顺序，每一步失败都能回到起点：
//
//	1. 完整 inspect 旧容器（拿到可复制的配置与当前运行状态）
//	2. 需要则 pull 镜像            —— 失败：旧容器未动
//	3. 正在运行则停止             —— 必须停：不停则端口被占，新容器必然起不来
//	4. 旧容器改名为 <名>-arkade-old-<随机>  —— 腾出名字，但对象还在
//	5. 用原名创建新容器            —— 失败：旧容器改回原名并恢复原状态
//	6. 原来在运行则启动新容器       —— 失败：删掉半成品，旧容器改回原名并恢复原状态
//	7. 删除旧容器                  —— 失败：新容器已经就位，只记日志不回滚
//
// 第 3 步"先停"看起来可惜（本来可以少一次停机），但它是端口绑定的硬约束：
// 宿主端口只能被一个容器占用，旧容器还在跑时新容器启动会报
// "port is already allocated"。先停再做，是唯一能保证成功的顺序。
//
// 第 6 步是**有条件的**：原来停着的容器，重建完仍然是停止状态。
// 重建的目的是"换配置/换镜像"，不是"顺手启动"；擅自启动会让用户
// 在一个本该停着的服务上看到它突然开始对外提供服务。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ContainerRecreateRuntime 是可选能力：按原配置重建容器。
//
// 沿用可选接口的做法（见 ContainerInspectRuntime），**不**扩张既有 Runtime 接口：
// 扩张会让所有实现（含测试里的假运行时）都必须实现它，而那些假实现
// 根本不关心重建。断言失败时处理器返回 unsupported，提示比"注册了却永远失败"清楚。
type ContainerRecreateRuntime interface {
	// RecreateContainer 重建容器并返回新容器 ID。
	//
	// pull 为 true 时先拉取镜像最新版本；容器当前没有镜像引用（Image 为空）时
	// 不拉取——那说明它不是用镜像名创建的，拉什么都是猜的。
	RecreateContainer(ctx context.Context, idOrName string, pull bool) (string, error)
}

// ============================ 要复制的配置（显式白名单） ============================
//
// 为什么用白名单而不是把 inspect 结果整体回灌：
// Docker 的 inspect 输出里混着**只读元数据**（Id、Created、State、Image 摘要）
// 与**创建期参数**（Env、Binds、RestartPolicy）。整体回灌会把只读字段
// 也塞进 create 请求，引擎要么报错、要么静默忽略一部分——两种结果都不可控。
// 白名单写明"复制什么"，也就写明了"什么不复制"，评审时能逐项对照。
//
// 有意不复制：
//   - Id / Created / State / RestartCount：由引擎生成，复制没有意义；
//   - LogPath / ResolvConfPath 等宿主机路径：引擎自己维护；
//   - 匿名卷的**数据内容**：卷本身按名字保留复用（见 recreateMounts），
//     但容器根文件系统里的临时文件必然重来，这就是界面上要提示"非持久化数据会丢失"的原因。

// recreateBody 既是读取 inspect 时 Config 的目标，也是 create 请求的顶层结构。
// 两者字段高度重合（Docker 的 create 请求体本来就等于 Config + HostConfig），
// 复用一份定义可以避免"读的字段名"和"写的字段名"悄悄不一致。
type recreateBody struct {
	Hostname     string              `json:"Hostname,omitempty"`
	Domainname   string              `json:"Domainname,omitempty"`
	User         string              `json:"User,omitempty"`
	Env          []string            `json:"Env,omitempty"`
	Cmd          []string            `json:"Cmd,omitempty"`
	Entrypoint   []string            `json:"Entrypoint,omitempty"`
	WorkingDir   string              `json:"WorkingDir,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	Tty          bool                `json:"Tty,omitempty"`
	OpenStdin    bool                `json:"OpenStdin,omitempty"`
	StdinOnce    bool                `json:"StdinOnce,omitempty"`
	StopSignal   string              `json:"StopSignal,omitempty"`
	Healthcheck  *recreateHealth     `json:"Healthcheck,omitempty"`
	Image        string              `json:"Image"`
	// HostConfig 与 NetworkingConfig 只在 create 请求里有意义；
	// 解码 inspect 的 Config 时它们保持零值，随后被显式赋值。
	HostConfig       recreateHostConfig `json:"HostConfig"`
	NetworkingConfig *recreateNetwork   `json:"NetworkingConfig,omitempty"`
}

// recreateHostConfig 是宿主侧配置的白名单。
//
// 这些字段决定了容器的对外行为（端口、挂载、重启策略、网络、权限、资源上限），
// 漏一项就会让重建出来的容器"看起来一样、跑起来不一样"，所以按 Docker API
// 里会实际影响运行的那部分逐项列出。
type recreateHostConfig struct {
	Binds          []string                      `json:"Binds,omitempty"`
	Mounts         []recreateMount               `json:"Mounts,omitempty"`
	PortBindings   map[string][]recreatePortBind `json:"PortBindings,omitempty"`
	RestartPolicy  recreateRestartPolicy         `json:"RestartPolicy"`
	NetworkMode    string                        `json:"NetworkMode,omitempty"`
	AutoRemove     bool                          `json:"AutoRemove,omitempty"`
	Privileged     bool                          `json:"Privileged,omitempty"`
	ReadonlyRootfs bool                          `json:"ReadonlyRootfs,omitempty"`
	Init           *bool                         `json:"Init,omitempty"`
	CapAdd         []string                      `json:"CapAdd,omitempty"`
	CapDrop        []string                      `json:"CapDrop,omitempty"`
	SecurityOpt    []string                      `json:"SecurityOpt,omitempty"`
	Devices        []recreateDevice              `json:"Devices,omitempty"`
	LogConfig      recreateLogConfig             `json:"LogConfig"`
	Dns            []string                      `json:"Dns,omitempty"`
	DnsSearch      []string                      `json:"DnsSearch,omitempty"`
	ExtraHosts     []string                      `json:"ExtraHosts,omitempty"`
	GroupAdd       []string                      `json:"GroupAdd,omitempty"`
	ShmSize        int64                         `json:"ShmSize,omitempty"`
	Tmpfs          map[string]string             `json:"Tmpfs,omitempty"`
	Ulimits        []recreateUlimit              `json:"Ulimits,omitempty"`
	Memory         int64                         `json:"Memory,omitempty"`
	MemorySwap     int64                         `json:"MemorySwap,omitempty"`
	NanoCpus       int64                         `json:"NanoCpus,omitempty"`
	CpuShares      int64                         `json:"CpuShares,omitempty"`
	CpuQuota       int64                         `json:"CpuQuota,omitempty"`
	CpuPeriod      int64                         `json:"CpuPeriod,omitempty"`
	CpusetCpus     string                        `json:"CpusetCpus,omitempty"`
	PidsLimit      *int64                        `json:"PidsLimit,omitempty"`
	OomKillDisable *bool                         `json:"OomKillDisable,omitempty"`
}

// recreateMount 对应 HostConfig.Mounts（--mount 语法创建的容器）。
type recreateMount struct {
	Type     string `json:"Type"`
	Source   string `json:"Source,omitempty"`
	Target   string `json:"Target"`
	ReadOnly bool   `json:"ReadOnly,omitempty"`
}

type recreatePortBind struct {
	HostIP   string `json:"HostIp,omitempty"`
	HostPort string `json:"HostPort,omitempty"`
}

type recreateRestartPolicy struct {
	Name              string `json:"Name"`
	MaximumRetryCount int    `json:"MaximumRetryCount,omitempty"`
}

type recreateDevice struct {
	PathOnHost        string `json:"PathOnHost"`
	PathInContainer   string `json:"PathInContainer"`
	CgroupPermissions string `json:"CgroupPermissions"`
}

type recreateLogConfig struct {
	Type   string            `json:"Type"`
	Config map[string]string `json:"Config,omitempty"`
}

type recreateUlimit struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

type recreateHealth struct {
	Test        []string `json:"Test,omitempty"`
	Interval    int64    `json:"Interval,omitempty"`
	Timeout     int64    `json:"Timeout,omitempty"`
	Retries     int      `json:"Retries,omitempty"`
	StartPeriod int64    `json:"StartPeriod,omitempty"`
}

// recreateNetwork 对应 create 请求的 NetworkingConfig。
//
// 必须显式重建：不传时新容器只会按 NetworkMode 接入默认网络，
// 原来接的自定义网络、网络别名都会丢——对"容器之间靠服务名互访"的部署
// 这是致命差异，而且不会有任何报错，只有连不上。
type recreateNetwork struct {
	EndpointsConfig map[string]recreateEndpoint `json:"EndpointsConfig,omitempty"`
}

type recreateEndpoint struct {
	Aliases []string `json:"Aliases,omitempty"`
}

// recreateInspect 是重建需要的 inspect 视图。
//
// 只解出重建要用的三段：Config（创建期参数）、HostConfig（宿主侧配置）、
// NetworkSettings.Networks（网络接入与别名）。其余字段一概不读，
// 避免把无关元数据带进 create 请求。
type recreateInspect struct {
	ID         string                 `json:"Id"`
	Name       string                 `json:"Name"`
	Config     recreateBody           `json:"Config"`
	HostConfig recreateHostConfig     `json:"HostConfig"`
	Mounts     []recreateInspectMount `json:"Mounts"`
	// State 只取 Running：重建要**保持原来的运行状态**。
	// 不读它就会出现"重建一个已停止的容器，它却自己跑起来了"——
	// 对用户来说这是最不想看到的意外，尤其是他本来就是为了先停掉它。
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	NetworkSettings struct {
		Networks map[string]struct {
			Aliases []string `json:"Aliases"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

// recreateInspectMount 是 inspect 顶层 Mounts 的元素（已解析的挂载）。
type recreateInspectMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
	ReadOnly    bool   `json:"ReadOnly"`
}

// RecreateContainer 按原配置重建容器，返回新容器 ID。
func (e *engine) RecreateContainer(ctx context.Context, idOrName string, pull bool) (string, error) {
	if err := validateRef(idOrName); err != nil {
		return "", err
	}

	var old recreateInspect
	if err := e.client.getJSON(ctx, containerPath(idOrName)+"/json", &old); err != nil {
		return "", e.translate(err)
	}
	name := strings.TrimPrefix(old.Name, "/")
	if name == "" {
		return "", errors.New("无法从引擎获取容器名称，拒绝重建无名容器")
	}
	// 防御：helper 是 Arkade 自己的内部容器，重建它们只会制造残留。
	// 正常调用路径永远不会指向它们，这里挡住的是误用/脚本化调用。
	if isHelperName(name) {
		return "", fmt.Errorf("拒绝重建内部临时容器 %q", name)
	}

	// 第 2 步：需要时先拉镜像。
	//
	// 必须在**动旧容器之前**做：拉取可能耗时长且容易失败（网络、鉴权、磁盘），
	// 放在停容器之后就会变成"停了一个小时还没拉起新版本"。
	if pull {
		if ref := strings.TrimSpace(old.Config.Image); ref != "" {
			if _, perr := e.PullImage(ctx, ref, "", "", nil); perr != nil {
				return "", fmt.Errorf("拉取镜像 %s 失败: %w", ref, perr)
			}
		}
	}

	body, err := json.Marshal(recreateRequest(&old))
	if err != nil {
		return "", fmt.Errorf("序列化重建配置失败: %w", err)
	}

	// 记住原来的运行状态：重建后要还原它。
	// 已停止的容器重建完仍是停止状态，否则用户会以为"我只想换个镜像，怎么自己跑起来了"。
	wasRunning := old.State.Running

	// 第 3 步：停旧容器（端口冲突的硬约束，见文件头注释）。
	// 已停止的容器不需要再停，跳过也少一次无意义的引擎调用。
	if wasRunning {
		if err := e.Stop(ctx, old.ID, false); err != nil {
			return "", fmt.Errorf("重建前停止容器失败: %w", err)
		}
	}

	backup := recreateBackupName(name)

	// 回滚动作：把旧容器改回原名，并恢复它原来的运行状态。
	//
	// 用 WithoutCancel：走到回滚时往往正是因为 ctx 被取消/超时，
	// 若继续用同一个 ctx，回滚本身也会被取消，那就留下一个名字被改掉、
	// 状态是停止的容器——比不做回滚更难收拾。
	rollback := func() error {
		rbCtx := context.WithoutCancel(ctx)
		var errs []string
		if err := e.renameContainer(rbCtx, old.ID, name); err != nil {
			errs = append(errs, "改回原名失败: "+err.Error())
		}
		// 原本就没在运行，就不要"顺手"把它启动起来。
		if wasRunning {
			if err := e.Start(rbCtx, old.ID); err != nil {
				errs = append(errs, "重新启动失败: "+err.Error())
			}
		}
		if len(errs) > 0 {
			return errors.New(strings.Join(errs, "；"))
		}
		return nil
	}

	// 第 4 步：改名腾出名字。
	if err := e.renameContainer(ctx, old.ID, backup); err != nil {
		// 改名失败说明旧容器没被改坏，但仍处于"已停止"状态，必须把它拉起来。
		if wasRunning {
			if rerr := e.Start(context.WithoutCancel(ctx), old.ID); rerr != nil {
				return "", fmt.Errorf("重命名旧容器失败: %w（且重新启动失败: %v）", err, rerr)
			}
		}
		return "", fmt.Errorf("重命名旧容器失败: %w", err)
	}

	// 第 5 步：用原名建新容器。
	newID, err := e.createContainer(ctx, name, body)
	if err != nil {
		if rerr := rollback(); rerr != nil {
			return "", fmt.Errorf("创建新容器失败: %w（回滚也失败: %v）", err, rerr)
		}
		return "", fmt.Errorf("创建新容器失败: %w", err)
	}

	// 第 6 步：原本在运行的话，把新容器启动起来。
	if wasRunning {
		if err := e.Start(ctx, newID); err != nil {
			// 半成品必须删掉，否则会留下一个"没在跑但占着名字"的容器，
			// 让用户下一次操作时报名字冲突，且看不出是它。
			rmCtx := context.WithoutCancel(ctx)
			_ = e.RemoveContainer(rmCtx, newID, true, false)
			if rerr := rollback(); rerr != nil {
				return "", fmt.Errorf("启动新容器失败: %w（回滚也失败: %v）", err, rerr)
			}
			return "", fmt.Errorf("启动新容器失败: %w", err)
		}
	}

	// 第 7 步：删旧容器。
	//
	// 到这一步新容器已经就位，旧容器只是残留。删除失败**不回滚**：
	// 回滚意味着把刚建好的新容器删掉、换回旧容器，等于把已经成功的重建
	// 退回去；留一个 <名>-arkade-old-xxxx 的残留容器反而是更小的损失，
	// 而且它的名字本身就是"这是残留"的提示。因此这里只记录不中断。
	// （日志交给上层：runtime 层不持有 logger，见 engine 的既有约定。）
	_ = e.RemoveContainer(context.WithoutCancel(ctx), old.ID, true, false)

	return newID, nil
}

// recreateRequest 把 inspect 结果整理成 create 请求体。
//
// 独立成函数便于单测直接验证"复制了哪些字段"，不必真的连引擎。
func recreateRequest(old *recreateInspect) recreateBody {
	body := old.Config // 拷贝一份，后续只改需要覆盖的字段
	body.HostConfig = old.HostConfig
	body.NetworkingConfig = recreateNetworkConfig(old)
	body.HostConfig.Mounts = recreateMounts(old)
	return body
}

// recreateMounts 还原挂载配置。
//
// 两种创建方式要分别处理，否则会丢挂载：
//   - `-v` / Binds：inspect 里 Binds 有值，原样带过去即可；
//   - `--mount`：Binds 为空，参数在 HostConfig.Mounts 里；
//     再退回顶层已解析的 Mounts（老版本 API 可能不回填 HostConfig.Mounts）。
//
// 具名卷与匿名卷都按**卷名**复用（Source 就是卷名）：
// 这样重建后数据还在，符合用户预期；容器根文件系统里的非卷数据仍然会丢，
// 界面文案要如实说明这一条。
func recreateMounts(old *recreateInspect) []recreateMount {
	if len(old.HostConfig.Binds) > 0 {
		// 用 Binds 创建时不能再传 Mounts：两种语法混用会被引擎拒绝。
		return nil
	}
	if len(old.HostConfig.Mounts) > 0 {
		return old.HostConfig.Mounts
	}
	if len(old.Mounts) == 0 {
		return nil
	}
	out := make([]recreateMount, 0, len(old.Mounts))
	for _, m := range old.Mounts {
		// tmpfs 与 bind 的 Source 语义不同：tmpfs 没有源，传空即可。
		out = append(out, recreateMount{
			Type:     m.Type,
			Source:   m.Source,
			Target:   m.Destination,
			ReadOnly: m.ReadOnly,
		})
	}
	return out
}

// recreateNetworkConfig 还原网络接入与别名。
//
// 只在"用户自定义网络"这种需要显式接入的场景下返回配置：
// host / none / container:<id> 这几种模式下，容器不接入管理网络，
// 传 EndpointsConfig 反而会被引擎拒绝。
func recreateNetworkConfig(old *recreateInspect) *recreateNetwork {
	mode := old.HostConfig.NetworkMode
	if mode == "host" || mode == "none" || strings.HasPrefix(mode, "container:") {
		return nil
	}
	if len(old.NetworkSettings.Networks) == 0 {
		return nil
	}
	eps := make(map[string]recreateEndpoint, len(old.NetworkSettings.Networks))
	for name, s := range old.NetworkSettings.Networks {
		eps[name] = recreateEndpoint{Aliases: s.Aliases}
	}
	return &recreateNetwork{EndpointsConfig: eps}
}

// recreateBackupName 生成旧容器的临时名字。
//
// 加随机后缀而不是固定 "-arkade-old"：同一次重建若因故重试，
// 固定名字会与上一次的残留撞名，让"改名"这一步先失败。
func recreateBackupName(name string) string {
	return name + "-arkade-old-" + shortID()
}

// renameContainer 调用引擎的重命名端点。
func (e *engine) renameContainer(ctx context.Context, idOrName, newName string) error {
	if strings.TrimSpace(newName) == "" {
		return errors.New("新容器名不能为空")
	}
	_, err := e.client.do(ctx, http.MethodPost,
		containerPath(idOrName)+"/rename?name="+url.QueryEscape(newName), nil)
	if err != nil {
		return e.translate(err)
	}
	return nil
}

// createContainer 按给定名字与请求体创建容器，返回新容器 ID。
func (e *engine) createContainer(ctx context.Context, name string, body []byte) (string, error) {
	data, err := e.client.do(ctx, http.MethodPost,
		pathContainersCreate+"?name="+url.QueryEscape(name), strings.NewReader(string(body)))
	if err != nil {
		return "", e.translate(err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	if json.Unmarshal(data, &created) != nil || created.ID == "" {
		return "", errors.New("创建容器失败：引擎返回无法解析")
	}
	return created.ID, nil
}

// isHelperName 判断名字是否属于 Arkade 的内部临时容器。
//
// 判据取自 helper_reaper.go 的同一组前缀常量：
// 两处若各写一份，改了一处就会漏另一处。
func isHelperName(name string) bool {
	return strings.HasPrefix(name, volumeHelperNamePrefix) ||
		strings.HasPrefix(name, composeHelperNamePrefix)
}
