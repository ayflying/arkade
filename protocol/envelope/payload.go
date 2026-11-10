package envelope

// 各方法共用的载荷结构。放在这里而不是 hub/agent 内部，是为了让两端共享字段定义，
// 任何字段改动都会同时影响两端编译，不会出现只在一边改了却忘记另一边的情况。

// RuntimeKind 运行时类型。首期只支持 docker 与 podman（决策 D-01）。
type RuntimeKind string

const (
	RuntimeDocker RuntimeKind = "docker"
	RuntimePodman RuntimeKind = "podman"
	RuntimeNone   RuntimeKind = "none" // 未探测到可用运行时
)

// RuntimeInfo 描述 Agent 所在机器的运行时情况。
type RuntimeInfo struct {
	Kind     RuntimeKind `json:"kind"`
	Version  string      `json:"version"`  // 如 27.3.1
	APIVer   string      `json:"api_ver"`  // 运行时 HTTP API 版本
	Socket   string      `json:"socket"`   // 使用的套接字或端点（脱敏后的路径）
	Rootless bool        `json:"rootless"` // Podman 常见 rootless 模式，影响卷与权限行为
}

// HelloPayload 是 Agent 上线握手请求。
type HelloPayload struct {
	AgentVersion string      `json:"agent_version"`
	ProtoVersion int         `json:"proto_version"`
	Runtime      RuntimeInfo `json:"runtime"`
	Capabilities []string    `json:"capabilities"` // 能力清单，Hub 据此决定下发哪些方法
	Hostname     string      `json:"hostname"`
	OS           string      `json:"os"`
	Arch         string      `json:"arch"`
}

// HelloAckPayload 是 Hub 对握手的应答，用于把 Hub 侧分配的信息回传。
type HelloAckPayload struct {
	AgentID      string `json:"agent_id"`
	HubVersion   string `json:"hub_version"`
	HeartbeatSec int    `json:"heartbeat_sec"` // Hub 要求的心跳间隔，避免两端硬编码不一致
}

// HeartbeatPayload 是心跳载荷，只带摘要，不带全量容器列表，避免高频消息过大。
type HeartbeatPayload struct {
	AgentID          string `json:"agent_id"`
	ContainerTotal   int    `json:"container_total"`
	ContainerRunning int    `json:"container_running"`
	At               int64  `json:"at"`
}

// ContainerSummary 是容器列表项。只保留界面需要的字段，不搬运运行时全量结构。
type ContainerSummary struct {
	ID     string `json:"id"`     // 短 ID（12 位），足够界面展示与后续操作
	Name   string `json:"name"`   // 容器名（去掉运行时添加的前导斜杠）
	Image  string `json:"image"`  // 镜像引用
	State  string `json:"state"`  // running / exited / paused / created …
	Status string `json:"status"` // 人类可读状态文本
	Origin string `json:"origin"` // arkade / adopted / discovered，见 01 架构 4.2
	// StackID 是**兼容字段名**，实际填的是 compose 项目名
	// （容器标签 com.docker.compose.project），不是 Hub 数据库里的编排 ID。
	//
	// 名字沿用是为了不破坏既有前端与测试的字段引用。
	// 手工 docker run 启动的容器没有该标签，此处为空字符串。
	StackID string `json:"stack_id"`
	Created int64  `json:"created"` // 创建时间 Unix 秒
	Ports   string `json:"ports"`   // 端口映射摘要文本
}

// ContainerListResult 是容器列表应答。
type ContainerListResult struct {
	AgentID    string             `json:"agent_id"`
	Containers []ContainerSummary `json:"containers"`
}

// ContainerOpPayload 是启停、重启、删除等针对单个容器的操作请求。
type ContainerOpPayload struct {
	ContainerID string `json:"container_id"`
	// Force 在删除时表示强制（连运行中的容器一起删），在停止时表示不等待优雅退出。
	Force bool `json:"force"`
	// RemoveVolumes 仅对删除有效：是否同时删除容器的匿名数据卷。
	//
	// 放在协议层而不是 Hub 与 Agent 各写一份，是因为这是一个跨模块契约：
	// 两边字段名一旦不一致（比如一边写 remove_volumes、另一边写 remove_volume），
	// 解码会静默取到零值 false，用户以为删了卷其实没删——这种失败没有任何报错，
	// 只能靠人工发现数据还在。共享结构体让编译期就暴露不一致。
	//
	// 默认 false 是刻意的：删卷是数据丢失操作，必须由调用方显式要求。
	RemoveVolumes bool `json:"remove_volumes"`
}

// ContainerLogsPayload 是日志请求。行数必须有上限：日志是无界数据，
// 无上限拉取会占满 WebSocket 队列并拖慢整条连接（见 03 技术选型）。
type ContainerLogsPayload struct {
	ContainerID string `json:"container_id"`
	TailLines   int    `json:"tail_lines"`
}

// ContainerLogsResult 是日志应答。
type ContainerLogsResult struct {
	Lines     []string `json:"lines"`
	Truncated bool     `json:"truncated"` // 是否因行数上限被截断
}

// ContainerOpResult 是启停结果，回传最新状态，前端无需再发一次列表请求。
type ContainerOpResult struct {
	ContainerID string `json:"container_id"`
	State       string `json:"state"`
}
