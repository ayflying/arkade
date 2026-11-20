package v1

import (
	"arkade/hub/internal/store"
	"github.com/gogf/gf/v2/frame/g"
)

type ContainerMetricsReq struct {
	g.Meta  `path:"/api/agents/{agentID}/container-metrics" method:"get" tags:"Resources"`
	AgentID string `json:"-" in:"path"`
}

type ContainerMetricsRes struct {
	AgentID           string            `json:"agent_id"`
	StaleAfterSeconds int64             `json:"stale_after_seconds"`
	Containers        []ContainerMetric `json:"containers"`
}

// ContainerMetricHistoryReq 查询单个容器的资源历史。
// from/to 是 Unix 秒，服务端会限制在保留期内并把跨度降采样到点数上限。
type ContainerMetricHistoryReq struct {
	g.Meta      `path:"/api/agents/{agentID}/containers/{containerID}/metrics/history" method:"get" tags:"Resources"`
	AgentID     string `json:"-" in:"path"`
	ContainerID string `json:"-" in:"path"`
	From        int64  `json:"from" in:"query"`
	To          int64  `json:"to" in:"query"`
}

type ContainerMetricHistoryRes struct {
	AgentID       string                     `json:"agent_id"`
	ContainerID   string                     `json:"container_id"`
	From          int64                      `json:"from"`
	To            int64                      `json:"to"`
	BucketSeconds int64                      `json:"bucket_seconds"`
	RetentionDays int                        `json:"retention_days"`
	Points        []store.MonitorSamplePoint `json:"points"`
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
