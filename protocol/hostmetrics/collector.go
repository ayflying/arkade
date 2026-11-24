// Package hostmetrics samples Linux host resources without querying the container runtime.
package hostmetrics

import (
	"os"
	"sync"
	"time"

	"arkade/protocol/envelope"
)

const (
	// HostDiskMountPath is the optional read-only mount of a dedicated host directory.
	HostDiskMountPath = "/run/arkade/host-disk"
	// NativeDiskEnv enables sampling of / for a deployment known to be native Linux.
	NativeDiskEnv = "ARKADE_HOST_DISK_NATIVE"
	// maxRateInterval 是两次采样之间允许的最大间隔。超过它就不再输出速率：
	// 采样中断（进程挂起、Agent 长时间离线）后按平均值算出来的「当前速率」
	// 会严重失真，宁可不显示，也不给一个看起来精确的假数字。
	maxRateInterval = 5 * time.Minute
)

// cpuTimes excludes guest and guest_nice, which Linux already counts in user/nice.
type cpuTimes [8]uint64

func cpuPercent(previous, current cpuTimes) *float64 {
	var busy, total float64
	for i, value := range current {
		if value < previous[i] {
			return nil
		}
		delta := float64(value - previous[i])
		total += delta
		if i != 3 && i != 4 { // idle and iowait are not CPU execution
			busy += delta
		}
	}
	if total == 0 {
		return nil
	}
	percent := 100 * (busy / total)
	return &percent
}

// counterPair 是一对累计计数器在某个时刻的快照，用来算两次采样之间的速率。
type counterPair struct {
	at time.Time
	a  uint64
	b  uint64
}

// rate 返回两次快照之间每秒的增量。
//
// 计数器回绕（重启、网卡重建）、时间不前进、间隔超过 maxRateInterval 时
// 返回 ok=false，由调用方省略该指标，而不是输出 0 或负值。
func rate(previous, current counterPair) (a, b uint64, ok bool) {
	if current.a < previous.a || current.b < previous.b {
		return 0, 0, false
	}
	seconds := current.at.Sub(previous.at).Seconds()
	if seconds <= 0 || seconds > maxRateInterval.Seconds() {
		return 0, 0, false
	}
	return uint64(float64(current.a-previous.a) / seconds), uint64(float64(current.b-previous.b) / seconds), true
}

// Collector holds the previous CPU counters. Reuse one instance across sampling
// intervals, and do not copy it after use. The zero value produces no samples.
type Collector struct {
	mu          sync.Mutex
	previousCPU *cpuTimes
	previousNet *counterPair
	previousIO  *counterPair
	readCPU     func() *cpuTimes
	readMemory  func() *envelope.ResourceUsage
	readDisk    func() *envelope.DiskUsage
	readNet     func(at time.Time) *counterPair
	readDiskIO  func(at time.Time) *counterPair
}

// NewCollector builds a collector for the current OS. Disk capacity reporting
// requires an explicit read-only mount at HostDiskMountPath or NativeDiskEnv=1
// on native Linux; network and disk IO rates work from procfs by default.
func NewCollector() *Collector {
	return newCollector(os.Getenv(NativeDiskEnv) == "1")
}

// Sample returns the available metrics, or nil if none are available. CPU,
// network and disk IO rates require two consecutive valid samples; a bad read or
// counter reset does not reuse an old value. On non-Linux platforms Sample
// always returns nil.
func (c *Collector) Sample() *envelope.HostMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	var sample envelope.HostMetrics
	if c.readCPU != nil {
		current := c.readCPU()
		if c.previousCPU != nil && current != nil {
			sample.CPUPercent = cpuPercent(*c.previousCPU, *current)
		}
		c.previousCPU = current
	}
	if c.readMemory != nil {
		sample.Memory = c.readMemory()
	}
	if c.readDisk != nil {
		sample.Disk = c.readDisk()
	}
	if c.readNet != nil {
		current := c.readNet(now)
		if current != nil && c.previousNet != nil {
			if rx, tx, ok := rate(*c.previousNet, *current); ok {
				sample.Network = &envelope.NetworkUsage{RxBytesPerSec: rx, TxBytesPerSec: tx}
			}
		}
		c.previousNet = current
	}
	if c.readDiskIO != nil {
		current := c.readDiskIO(now)
		if current != nil && c.previousIO != nil {
			if read, write, ok := rate(*c.previousIO, *current); ok {
				sample.DiskIO = &envelope.DiskIOUsage{ReadBytesPerSec: read, WriteBytesPerSec: write}
			}
		}
		c.previousIO = current
	}
	if sample.CPUPercent == nil && sample.Memory == nil && sample.Disk == nil &&
		sample.Network == nil && sample.DiskIO == nil {
		return nil
	}
	return &sample
}
