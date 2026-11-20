// =================================================================================
// Code generated and maintained by GoFrame CLI tool. DO NOT EDIT.
// =================================================================================

package do

import (
	"github.com/gogf/gf/v2/frame/g"
)

// MonitorSamples is the golang structure of table monitor_samples for DAO operations like Where/Data.
type MonitorSamples struct {
	g.Meta              `orm:"table:monitor_samples, do:true"`
	AgentId             any //
	ContainerId         any //
	Project             any //
	ContainerName       any //
	BucketAt            any //
	LastAt              any //
	State               any //
	StatsAvailable      any //
	CpuPercent          any //
	MemoryBytes         any //
	MemoryLimitBytes    any //
	MemoryPercent       any //
	NetworkAvailable    any //
	NetworkRxPerSecond  any //
	NetworkTxPerSecond  any //
	BlockAvailable      any //
	BlockReadPerSecond  any //
	BlockWritePerSecond any //
	RatesAvailable      any //
	SnapshotComplete    any //
}
