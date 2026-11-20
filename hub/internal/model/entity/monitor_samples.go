// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package entity

// MonitorSamples is the golang structure for table monitor_samples.
type MonitorSamples struct {
	AgentId             string  `json:"agentId"             orm:"agent_id"              ` //
	ContainerId         string  `json:"containerId"         orm:"container_id"          ` //
	Project             string  `json:"project"             orm:"project"               ` //
	ContainerName       string  `json:"containerName"       orm:"container_name"        ` //
	BucketAt            int64   `json:"bucketAt"            orm:"bucket_at"             ` //
	LastAt              int64   `json:"lastAt"              orm:"last_at"               ` //
	State               string  `json:"state"               orm:"state"                 ` //
	StatsAvailable      bool    `json:"statsAvailable"      orm:"stats_available"       ` //
	CpuPercent          float64 `json:"cpuPercent"          orm:"cpu_percent"           ` //
	MemoryBytes         int64   `json:"memoryBytes"         orm:"memory_bytes"          ` //
	MemoryLimitBytes    int64   `json:"memoryLimitBytes"    orm:"memory_limit_bytes"    ` //
	MemoryPercent       float64 `json:"memoryPercent"       orm:"memory_percent"        ` //
	NetworkAvailable    bool    `json:"networkAvailable"    orm:"network_available"     ` //
	NetworkRxPerSecond  float64 `json:"networkRxPerSecond"  orm:"network_rx_per_second" ` //
	NetworkTxPerSecond  float64 `json:"networkTxPerSecond"  orm:"network_tx_per_second" ` //
	BlockAvailable      bool    `json:"blockAvailable"      orm:"block_available"       ` //
	BlockReadPerSecond  float64 `json:"blockReadPerSecond"  orm:"block_read_per_second" ` //
	BlockWritePerSecond float64 `json:"blockWritePerSecond" orm:"block_write_per_second"` //
	RatesAvailable      bool    `json:"ratesAvailable"      orm:"rates_available"       ` //
	SnapshotComplete    bool    `json:"snapshotComplete"    orm:"snapshot_complete"     ` //
}
