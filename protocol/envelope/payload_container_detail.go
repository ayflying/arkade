package envelope

// payload_container_detail.go：容器详情（container.inspect）与端口映射。
//
// ============================ 为什么需要它 ============================
//
// 列表只给"状态文本 + 端口摘要字符串"，用户能看见容器在跑，
// 但看不到它到底挂载了什么、在哪个网络、退出码是多少、资源上限多少。
// 排查问题（"为什么重启了"、"数据挂到哪去了"）必须要有这些字段。
//
// ============================ 安全裁剪是硬约束 ============================
//
// 本次**只返回白名单字段**，绝不做"把 daemon 的 inspect 原样转发"：
//   - 不回 Env：环境变量里几乎必然有密码、token、连接串；
//   - 不回全部 Labels：用户自定义标签可能含敏感信息；
//   - 不回 daemon 原始 JSON：里面有 Secrets、Configs、HostConfig 的内部细节。
//
// 网络地址、挂载源路径、command 参数同样可能带敏感信息，
// 因此这三类的可见性由 Hub 侧按"是否 manage"控制（Agent 只负责如实提供）。

// PublishedPort 是一条**实际的端口发布**（宿主机端口 → 容器端口）。
//
// 只包含真正映射到宿主机端口的条目：容器内 EXPOSE 但未发布到宿主的端口
// 没有 HostPort，属于"内部暴露"，放在 ExposedPorts 里，不混进来。
//
// 之所以不沿用列表里的 "0.0.0.0:8080->80/tcp" 摘要字符串：
// 那种文本在前端只能整块展示，无法排序、筛选、判断冲突，
// 也没法让用户点一下就知道"宿主机 8080 被谁占了"。
type PublishedPort struct {
	HostIP        string `json:"host_ip"`
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
	Protocol      string `json:"protocol"` // tcp / udp / sctp
}

// ContainerNetwork 是容器接入的一个网络。
//
// 只给名字与容器在该网络里的地址，不给网关、子网、DNS 等内部拓扑，
// 那些信息对"看这个容器在哪"没有帮助，却会暴露宿主机网络布局。
//
// ============================ 为什么带 IPv6 ============================
//
// Docker 的 EndpointSettings 同时给 IPAddress 与 GlobalIPv6Address，
// 双栈网络下只显示 IPv4 会让用户以为没有 IPv6 地址。
// 但 IPv6 在多数部署里为空，所以用 omitempty——空值不出现在协议里，
// 前端不需要判断"这个空字符串是不是代表没有"。
//
// ============================ 空值的含义 ============================
//
// 两个地址都为空**不代表**容器没有网络，而是代表"该网络没有分配地址"：
// host 模式、network_mode: none 的容器就是这种形态。
// 消费方绝不能把空值兜底成 0.0.0.0 或宿主机地址——那是在编造事实。
type ContainerNetwork struct {
	Name string `json:"name"`
	// IPAddress 是 IPv4 地址；host/none 模式下为空字符串。
	IPAddress string `json:"ip_address"`
	// IPv6Address 是全局 IPv6 地址；非双栈或未分配时省略。
	IPv6Address string `json:"ipv6_address,omitempty"`
}

// ContainerMount 是容器的一个挂载点。
//
// Source 是宿主侧路径或卷名，属于敏感信息（能暴露目录结构），
// 由 Hub 侧决定给谁看。
type ContainerMount struct {
	Type        string `json:"type"` // bind / volume / tmpfs
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"read_only"`
}

// ContainerResourceLimits 是容器的资源上限。
//
// 0 表示"未设置上限"（daemon 的默认语义），不是"上限为 0"。
type ContainerResourceLimits struct {
	MemoryBytes int64  `json:"memory_bytes"`
	NanoCPUs    int64  `json:"nano_cpus"`
	PidsLimit   *int64 `json:"pids_limit,omitempty"`
}

// ContainerInspectPayload 是 container.inspect 的请求。
type ContainerInspectPayload struct {
	ContainerID string `json:"container_id"`
}

// ContainerInspectResult 是容器详情应答（**白名单字段**，不含 Env 与 Labels）。
type ContainerInspectResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Image   string `json:"image"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Created string `json:"created"`

	// StartedAt / FinishedAt 是 RFC3339 字符串；未启动/未结束时为零值字符串。
	StartedAt    string `json:"started_at"`
	FinishedAt   string `json:"finished_at"`
	ExitCode     int    `json:"exit_code"`
	RestartCount int    `json:"restart_count"`
	NetworkMode  string `json:"network_mode"`

	// Command 是容器的启动命令（Entrypoint + Cmd 合并）。
	// 可能含口令参数，属于敏感字段，由 Hub 侧控制可见性。
	Command []string `json:"command"`

	Networks       []ContainerNetwork `json:"networks"`
	Mounts         []ContainerMount   `json:"mounts"`
	PublishedPorts []PublishedPort    `json:"published_ports"`
	// ExposedPorts 是容器声明暴露但**未发布到宿主机**的端口（"80/tcp" 形式）。
	ExposedPorts []string `json:"exposed_ports"`

	ResourceLimits ContainerResourceLimits `json:"resource_limits"`
}
