package runtime

// container_detail.go：容器详情（container.inspect）的只读实现。
//
// ============================ 为什么单独一个文件 ============================
//
// 详情要解析一个**大而杂**的 daemon 结构（几十个字段，其中 Env/Secrets/
// Configs/HostConfig 内部细节都不该出去）。单独放一处，让"到底取了哪些字段"
// 在一屏内可审——混在 engine.go 里，评审时最容易漏看。
//
// ============================ 绝不整包转发 ============================
//
// 这里手工挑选字段并转换成本包的窄结构。**不做** "把 daemon JSON 原样塞给上层"：
// 那个结构里有 Env（几乎必然含密码/token）与 Secrets，一旦转发就再也收不回来。
// 加字段必须显式在这里写一行，这是刻意的摩擦。

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ContainerInspectRuntime 是可选能力：只读读取单个容器的详情。
//
// 沿用可选接口的做法，不改既有 Runtime 接口——已有 mock 无需改动。
type ContainerInspectRuntime interface {
	InspectContainer(ctx context.Context, idOrName string) (InspectDetail, error)
}

// InspectDetail 是容器详情的窄视图（只含 Arkade 需要的字段）。
type InspectDetail struct {
	ID           string
	Name         string
	Image        string
	State        string
	Status       string
	Created      string
	StartedAt    string
	FinishedAt   string
	ExitCode     int
	RestartCount int
	NetworkMode  string
	Command      []string
	Networks     []InspectNetwork
	Mounts       []InspectMount
	Ports        []Port // 复用列表的端口结构；Public>0 即已发布
	ExposedPorts []string
	MemoryBytes  int64
	NanoCPUs     int64
	PidsLimit    *int64
}

// InspectNetwork 是容器接入的一个网络。
type InspectNetwork struct {
	Name      string
	IPAddress string
}

// InspectMount 是一个挂载点。
type InspectMount struct {
	Type        string
	Source      string
	Destination string
	ReadOnly    bool
}

// inspectResp 是 /containers/{id}/json 的响应。
//
// 只声明用得上的字段。**刻意没有 Env / Labels / Secrets / Configs**——
// 不声明就不可能被意外传播出去。
type inspectResp struct {
	ID      string `json:"Id"`
	Name    string `json:"Name"`
	Image   string `json:"Image"`
	Created string `json:"Created"`
	State   struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`
	RestartCount int `json:"RestartCount"`
	Config       struct {
		Image      string              `json:"Image"`
		Cmd        []string            `json:"Cmd"`
		Entrypoint []string            `json:"Entrypoint"`
		Exposed    map[string]struct{} `json:"ExposedPorts"`
	} `json:"Config"`
	HostConfig struct {
		NetworkMode string `json:"NetworkMode"`
		Memory      int64  `json:"Memory"`
		NanoCPUs    int64  `json:"NanoCpus"`
		PidsLimit   *int64 `json:"PidsLimit"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Ports    map[string][]portBinding `json:"Ports"`
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
	Mounts []struct {
		Type        string `json:"Type"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`
}

// portBinding 是网络设置里的端口绑定。
type portBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// InspectContainer 读取单个容器的详情（只读）。
func (e *engine) InspectContainer(ctx context.Context, idOrName string) (InspectDetail, error) {
	if err := validateRef(idOrName); err != nil {
		return InspectDetail{}, err
	}
	var raw inspectResp
	path := pathContainerPrefix + url.PathEscape(idOrName) + "/json"
	if err := e.client.getJSON(ctx, path, &raw); err != nil {
		return InspectDetail{}, e.translate(err)
	}
	return toInspectDetail(raw), nil
}

// toInspectDetail 把 daemon 响应裁剪成窄视图。
func toInspectDetail(r inspectResp) InspectDetail {
	d := InspectDetail{
		ID:           r.ID,
		Name:         strings.TrimPrefix(r.Name, "/"),
		Image:        r.Config.Image,
		State:        r.State.Status,
		Status:       r.State.Status,
		Created:      r.Created,
		StartedAt:    r.State.StartedAt,
		FinishedAt:   r.State.FinishedAt,
		ExitCode:     r.State.ExitCode,
		RestartCount: r.RestartCount,
		NetworkMode:  r.HostConfig.NetworkMode,
		MemoryBytes:  r.HostConfig.Memory,
		NanoCPUs:     r.HostConfig.NanoCPUs,
		PidsLimit:    r.HostConfig.PidsLimit,
	}
	// 镜像字段回退：Config.Image 缺失时用顶层 Image（内容 ID）。
	if d.Image == "" {
		d.Image = r.Image
	}
	// 命令 = Entrypoint + Cmd（与 docker inspect 的 Path/Args 语义一致）。
	d.Command = append(append([]string{}, r.Config.Entrypoint...), r.Config.Cmd...)
	if len(d.Command) == 0 {
		d.Command = []string{}
	}

	// 网络：按名字排序前的收集。
	for name, n := range r.NetworkSettings.Networks {
		d.Networks = append(d.Networks, InspectNetwork{Name: name, IPAddress: n.IPAddress})
	}
	sortNetworks(d.Networks)

	// 挂载。
	for _, m := range r.Mounts {
		d.Mounts = append(d.Mounts, InspectMount{
			Type:        m.Type,
			Source:      m.Source,
			Destination: m.Destination,
			ReadOnly:    !m.RW,
		})
	}

	// 端口：一份数据两类用途。
	//   - 有有效绑定 → 已发布（HostPort 有效且容器端口可解析）；
	//   - 其余 → 仅内部暴露。
	//
	// 两类都要覆盖两个来源，否则会漏：
	//   - NetworkSettings.Ports 里有 spec 但绑定为 null / 空 HostPort
	//     （"声明了没发布"的常见形态）；
	//   - Config.ExposedPorts 里有但 NetworkSettings.Ports **整个键都缺失**
	//     （daemon 版本差异；老引擎只给 Exposed，不给空绑定）。
	//
	// 同时：容器端口解析失败（splitPortSpec 返回 0）的 spec 一律不进
	// Ports —— 否则会 append 一条 Private=0 的假映射，前端显示成"宿主端口
	// 映射到容器 0"，比丢弃更糟。这类 spec 也不能可靠地归一化，
	// 所以既不进已发布也不进仅暴露。
	publishedSpecs := make(map[string]struct{}, len(r.NetworkSettings.Ports))
	for spec, bindings := range r.NetworkSettings.Ports {
		containerPort, proto := splitPortSpec(spec)
		if containerPort == 0 {
			// 无法解析的 spec：跳过（见上，宁可丢弃）。
			continue
		}
		for _, b := range bindings {
			// 空 HostPort 表示"仅声明未发布"。
			if strings.TrimSpace(b.HostPort) == "" {
				continue
			}
			hp, perr := strconv.Atoi(b.HostPort)
			if perr != nil || hp <= 0 || hp > 65535 {
				// daemon 给了异常端口：丢弃这一条而不是回绕成错误值。
				continue
			}
			d.Ports = append(d.Ports, Port{
				IP:      b.HostIP,
				Private: containerPort,
				Public:  hp,
				Type:    proto,
			})
			publishedSpecs[normalizeSpec(containerPort, proto)] = struct{}{}
		}
	}
	// 合并去重：两个来源的 spec 统一归一化后放进一个 set。
	exposedSet := make(map[string]struct{})
	for spec := range r.NetworkSettings.Ports {
		n, proto := splitPortSpec(spec)
		if n == 0 {
			continue
		}
		key := normalizeSpec(n, proto)
		if _, published := publishedSpecs[key]; published {
			continue
		}
		exposedSet[key] = struct{}{}
	}
	for spec := range r.Config.Exposed {
		n, proto := splitPortSpec(spec)
		if n == 0 {
			continue
		}
		key := normalizeSpec(n, proto)
		if _, published := publishedSpecs[key]; published {
			continue
		}
		exposedSet[key] = struct{}{}
	}
	for key := range exposedSet {
		d.ExposedPorts = append(d.ExposedPorts, key)
	}
	sortPorts(d.Ports)
	sortStrings(d.ExposedPorts)
	return d
}

// normalizeSpec 是端口 spec 的规范形式（"80/tcp"）。
//
// 两个来源的写法可能不同（"80" vs "80/tcp"），
// 必须先归一化才能去重，否则同一个端口会出现两次。
func normalizeSpec(port int, proto string) string {
	if proto == "" {
		proto = "tcp"
	}
	return strconv.Itoa(port) + "/" + proto
}

// splitPortSpec 把 "80/tcp" 拆成 (80, "tcp")。
func splitPortSpec(spec string) (int, string) {
	portStr, proto, found := strings.Cut(spec, "/")
	if !found {
		proto = "tcp"
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return 0, proto
	}
	return p, proto
}

// sortPorts 排序：先容器端口，再协议，再宿主端口，最后宿主 IP。
//
// 最后一级 IP 不能省：一个端口常同时绑在 0.0.0.0 与 :: 上，
// 前三层完全相同。缺了 IP 这一级，两条记录的相对顺序由 map 遍历顺序决定，
// 详情页每次刷新都会在 IPv4/IPv6 之间来回跳。sort.Slice 不稳定，
// 即使输入集合相同也不保证输出相同。
func sortPorts(ps []Port) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Private != ps[j].Private {
			return ps[i].Private < ps[j].Private
		}
		if ps[i].Type != ps[j].Type {
			return ps[i].Type < ps[j].Type
		}
		if ps[i].Public != ps[j].Public {
			return ps[i].Public < ps[j].Public
		}
		return ps[i].IP < ps[j].IP
	})
}

// sortNetworks 按网络名排序，理由同上。
func sortNetworks(ns []InspectNetwork) {
	sort.Slice(ns, func(i, j int) bool { return ns[i].Name < ns[j].Name })
}

// sortStrings 对字符串切片排序。
func sortStrings(s []string) {
	sort.Strings(s)
}
