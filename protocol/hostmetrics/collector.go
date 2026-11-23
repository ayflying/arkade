// Package hostmetrics samples Linux host resources without querying the container runtime.
package hostmetrics

import (
	"os"
	"sync"

	"arkade/protocol/envelope"
)

const (
	// HostDiskMountPath is the optional read-only mount of a dedicated host directory.
	HostDiskMountPath = "/run/arkade/host-disk"
	// NativeDiskEnv enables sampling of / for a deployment known to be native Linux.
	NativeDiskEnv = "ARKADE_HOST_DISK_NATIVE"
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

// Collector holds the previous CPU counters. Reuse one instance across sampling
// intervals, and do not copy it after use. The zero value produces no samples.
type Collector struct {
	mu          sync.Mutex
	previousCPU *cpuTimes
	readCPU     func() *cpuTimes
	readMemory  func() *envelope.ResourceUsage
	readDisk    func() *envelope.DiskUsage
}

// NewCollector builds a collector for the current OS. Disk reporting requires an
// explicit read-only mount at HostDiskMountPath or NativeDiskEnv=1 on native Linux.
func NewCollector() *Collector {
	return newCollector(os.Getenv(NativeDiskEnv) == "1")
}

// Sample returns the available metrics, or nil if none are available. CPU
// requires two consecutive valid samples; a bad read or counter reset does not
// reuse an old percentage. On non-Linux platforms Sample always returns nil.
func (c *Collector) Sample() *envelope.HostMetrics {
	c.mu.Lock()
	defer c.mu.Unlock()

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
	if sample.CPUPercent == nil && sample.Memory == nil && sample.Disk == nil {
		return nil
	}
	return &sample
}
