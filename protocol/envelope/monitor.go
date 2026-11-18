package envelope

// MonitorSnapshotPayload 是只读监控请求；当前不接受过滤参数。
type MonitorSnapshotPayload struct{}

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
type ContainerMetric struct {
	ID               string  `json:"id"`
	Name             string  `json:"name"`
	Image            string  `json:"image"`
	State            string  `json:"state"`
	Health           string  `json:"health"`
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
