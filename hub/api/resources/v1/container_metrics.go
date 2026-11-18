package v1

import "github.com/gogf/gf/v2/frame/g"

type ContainerMetricsReq struct {
	g.Meta  `path:"/api/agents/{agentID}/container-metrics" method:"get" tags:"Resources"`
	AgentID string `json:"-" in:"path"`
}

type ContainerMetricsRes struct {
	AgentID           string            `json:"agent_id"`
	StaleAfterSeconds int64             `json:"stale_after_seconds"`
	Containers        []ContainerMetric `json:"containers"`
}

// ContainerMetric is a whitelist of the latest container measurements. Optional
// numbers distinguish unavailable values from a valid zero. At is Hub Unix time.
type ContainerMetric struct {
	ContainerID        string   `json:"container_id"`
	At                 int64    `json:"at"`
	Stale              bool     `json:"stale"`
	StatsAvailable     bool     `json:"stats_available"`
	CPUPercent         *float64 `json:"cpu_percent,omitempty"`
	MemoryBytes        *int64   `json:"memory_bytes,omitempty"`
	MemoryLimitBytes   *int64   `json:"memory_limit_bytes,omitempty"`
	MemoryPercent      *float64 `json:"memory_percent,omitempty"`
	NetworkAvailable   bool     `json:"network_available"`
	NetworkRXBytes     *uint64  `json:"network_rx_bytes,omitempty"`
	NetworkTXBytes     *uint64  `json:"network_tx_bytes,omitempty"`
	RatesAvailable     bool     `json:"rates_available"`
	NetworkRXPerSecond *float64 `json:"network_rx_per_second,omitempty"`
	NetworkTXPerSecond *float64 `json:"network_tx_per_second,omitempty"`
}
