package monitor

import (
	"math"
	"sort"
	"sync"
	"time"

	"arkade/protocol/envelope"
)

// Metric is a bounded, latest-value view, not a historical time series. ReceivedAt
// is Hub wall time; agent-supplied timestamps never determine freshness or rates.
type Metric struct {
	AgentID string `json:"agent_id"`
	envelope.ContainerMetric
	AgentName           string `json:"agent_name"`
	ContainerID         string `json:"container_id"`
	ReceivedAt          int64  `json:"at"`
	receivedMillis      int64
	SnapshotComplete    bool    `json:"snapshot_complete"`
	NetworkRXPerSecond  float64 `json:"network_rx_per_second"`
	NetworkTXPerSecond  float64 `json:"network_tx_per_second"`
	BlockReadPerSecond  float64 `json:"block_read_per_second"`
	BlockWritePerSecond float64 `json:"block_write_per_second"`
	RatesAvailable      bool    `json:"rates_available"`
}

type metricCache struct {
	mu     sync.RWMutex
	values map[string]Metric
}

func metricKey(agent, container string) string { return agent + "\x00" + container }
func counterRate(current, previous uint64, seconds float64) float64 {
	if current < previous || seconds <= 0 {
		return 0
	}
	return float64(current-previous) / seconds
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func (m *metricCache) put(agent string, c envelope.ContainerMetric, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = make(map[string]Metric)
	}
	key := metricKey(agent, c.ID)
	previous, exists := m.values[key]
	if !exists && len(m.values) >= 10000 {
		return
	}
	v := Metric{AgentID: agent, ContainerMetric: c, ContainerID: c.ID, ReceivedAt: now.Unix(), receivedMillis: now.UnixMilli()}
	seconds := float64(v.receivedMillis-previous.receivedMillis) / 1000
	if exists && previous.ContainerMetric.StatsAvailable && c.StatsAvailable && seconds > 0 {
		p := previous.ContainerMetric
		// Any reset invalidates this sample's rates instead of producing a spike.
		if c.NetworkRXBytes >= p.NetworkRXBytes && c.NetworkTXBytes >= p.NetworkTXBytes && c.BlockReadBytes >= p.BlockReadBytes && c.BlockWriteBytes >= p.BlockWriteBytes {
			v.RatesAvailable = true
			v.NetworkRXPerSecond = counterRate(c.NetworkRXBytes, p.NetworkRXBytes, seconds)
			v.NetworkTXPerSecond = counterRate(c.NetworkTXBytes, p.NetworkTXBytes, seconds)
			v.BlockReadPerSecond = counterRate(c.BlockReadBytes, p.BlockReadBytes, seconds)
			v.BlockWritePerSecond = counterRate(c.BlockWriteBytes, p.BlockWriteBytes, seconds)
		}
	}
	m.values[key] = v
}
func (m *metricCache) list() []Metric {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Metric, 0, len(m.values))
	for _, v := range m.values {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AgentID == out[j].AgentID {
			return out[i].ContainerID < out[j].ContainerID
		}
		return out[i].AgentID < out[j].AgentID
	})
	return out
}
func (m *metricCache) annotate(agent, container, name string, complete bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := metricKey(agent, container)
	v, ok := m.values[k]
	if ok {
		v.AgentName = name
		v.SnapshotComplete = complete
		m.values[k] = v
	}
}
func (m *metricCache) removeAgent(agent string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.values {
		if v.AgentID == agent {
			delete(m.values, k)
		}
	}
}
func (m *metricCache) removeContainer(agent, container string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.values, metricKey(agent, container))
}
func (m *metricCache) retainAgentContainers(agent string, seen map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, v := range m.values {
		if v.AgentID == agent && !seen[v.ContainerID] {
			delete(m.values, key)
		}
	}
}
func (m *metricCache) clear() { m.mu.Lock(); defer m.mu.Unlock(); m.values = nil }
func (m *metricCache) prune(before int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.values {
		if v.ReceivedAt < before {
			delete(m.values, k)
		}
	}
}
