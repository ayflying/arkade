package envelope

// MonitorSnapshotPayload 是只读监控请求。
//
// Scope 为 nil 表示旧行为：采集宿主上的全部容器（旧 Hub 不会下发该字段）。
// Scope 非 nil 表示按项目收窄采集范围，空项目集合意味着"什么都不采"，
// 而不是"不过滤"——两者必须能区分，否则"未接管任何项目"会退化成全量采集。
type MonitorSnapshotPayload struct {
	Scope *MonitorScope `json:"scope,omitempty"`
}

// MonitorScope 限定采集范围。Hub 下发 Arkade 已接管的项目名，Agent 用容器的
// com.docker.compose.project 标签匹配。旧 Agent 忽略该字段，仍按全量返回。
//
// 采集范围收窄必须由 Agent 执行：全量快照的代价是每容器一次 inspect 加一次
// stats，Hub 端过滤只能省存储，省不下宿主上的 Docker 调用。
type MonitorScope struct {
	Projects []string `json:"projects"`
}

// MonitorSnapshot 是某一时刻的采集结果。Complete=false 时不得把缺失容器视为已删除。
type MonitorSnapshot struct {
	At         int64             `json:"at"`
	Containers []ContainerMetric `json:"containers"`
	Complete   bool              `json:"complete"`
	Error      string            `json:"error"`
}

// ContainerMetric 只包含白名单指标，不包含环境变量或 inspect 原文。
// StatsAvailable governs CPU/memory. NetworkAvailable attests that network counters
// are attributable to this container; older agents do not send this flag.
//
// Project 是 compose 项目名，来自容器标签。旧 Agent 不填充该字段，
// Hub 据此判定"归属未知"，不把这类样本写入历史。
type ContainerMetric struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Image            string  `json:"image"`
	State            string  `json:"state"`
	Health           string  `json:"health"`
	Project          string  `json:"project,omitempty"`
	ExitCode         int     `json:"exit_code"`
	RestartCount     int     `json:"restart_count"`
	StartedAt        int64   `json:"started_at"`
	FinishedAt       int64   `json:"finished_at"`
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryBytes      int64   `json:"memory_bytes"`
	MemoryLimitBytes int64   `json:"memory_limit_bytes"`
	MemoryPercent    float64 `json:"memory_percent"`
	NetworkAvailable *bool   `json:"network_available,omitempty"`
	NetworkRXBytes   uint64  `json:"network_rx_bytes"`
	NetworkTXBytes   uint64  `json:"network_tx_bytes"`
	BlockAvailable   *bool   `json:"block_available,omitempty"`
	BlockReadBytes   uint64  `json:"block_read_bytes"`
	BlockWriteBytes  uint64  `json:"block_write_bytes"`
	StatsAvailable   bool    `json:"stats_available"`
	Error            string  `json:"error"`
}
