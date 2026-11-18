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
	networkRates        bool
	networkRxRate       float64
	networkTxRate       float64
	stale               bool
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
func (m *metricCache) put(agent string, c envelope.ContainerMetric, now time.Time, staleAfter int64) {
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
	consecutive := exists && !previous.Stale(now.Unix(), staleAfter) && seconds > 0
	if consecutive && attributedNetwork(c) && attributedNetwork(previous.ContainerMetric) &&
		c.StartedAt == previous.StartedAt && c.RestartCount == previous.RestartCount &&
		c.NetworkRXBytes >= previous.NetworkRXBytes && c.NetworkTXBytes >= previous.NetworkTXBytes {
		v.networkRates = true
		v.networkRxRate = counterRate(c.NetworkRXBytes, previous.NetworkRXBytes, seconds)
		v.networkTxRate = counterRate(c.NetworkTXBytes, previous.NetworkTXBytes, seconds)
	}
	p := previous.ContainerMetric
	// Preserve legacy admin rate semantics for old agents. New agents explicitly
	// identify unsupported counters, which the admin UI can hide independently.
	if consecutive && (c.StatsAvailable || c.BlockAvailable != nil) && (p.StatsAvailable || p.BlockAvailable != nil) {
		if c.NetworkRXBytes >= p.NetworkRXBytes && c.NetworkTXBytes >= p.NetworkTXBytes && c.BlockReadBytes >= p.BlockReadBytes && c.BlockWriteBytes >= p.BlockWriteBytes &&
			(c.BlockAvailable == nil || p.BlockAvailable == nil || *c.BlockAvailable == *p.BlockAvailable) &&
			(c.NetworkAvailable == nil || p.NetworkAvailable == nil || *c.NetworkAvailable == *p.NetworkAvailable) {
			v.RatesAvailable = true
			v.NetworkRXPerSecond = counterRate(c.NetworkRXBytes, p.NetworkRXBytes, seconds)
			v.NetworkTXPerSecond = counterRate(c.NetworkTXBytes, p.NetworkTXBytes, seconds)
			v.BlockReadPerSecond = counterRate(c.BlockReadBytes, p.BlockReadBytes, seconds)
			v.BlockWritePerSecond = counterRate(c.BlockWriteBytes, p.BlockWriteBytes, seconds)
		}
	}
	m.values[key] = v
}
func attributedNetwork(c envelope.ContainerMetric) bool {
	return c.NetworkAvailable != nil && *c.NetworkAvailable
}

// agentMetrics copies only the requested agent's rows before returning to HTTP.
func (m *metricCache) agentMetrics(agent string) []Metric {
	m.mu.RLock()
	out := make([]Metric, 0)
	for _, v := range m.values {
		if v.AgentID == agent {
			out = append(out, v)
		}
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ContainerID < out[j].ContainerID })
	return out
}

// markStale keeps the last sample but invalidates its use as a rate baseline.
func (m *metricCache) markStale(agent string, seen map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.values {
		if v.AgentID == agent && !seen[v.ContainerID] {
			v.stale = true
			m.values[k] = v
		}
	}
}

func (m *metricCache) retainAgents(active map[string]bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.values {
		if !active[v.AgentID] {
			delete(m.values, k)
		}
	}
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

// ContainerMetrics returns a scoped, latest-value view of the existing cache.
// A lookup never contacts the runtime or reads monitoring history.
func (e *Engine) ContainerMetrics(agent string) ([]Metric, int64) {
	after := e.staleAfter.Load()
	if after == 0 {
		after = metricStaleAfter(0)
	}
	return e.metrics.agentMetrics(agent), after
}

func (m Metric) Stale(now, after int64) bool {
	return m.stale || m.ReceivedAt > now || now-m.ReceivedAt >= after
}

func (m Metric) NetworkRates() (float64, float64, bool) {
	return m.networkRxRate, m.networkTxRate, m.networkRates
}

func metricStaleAfter(interval int) int64 {
	return 2*seconds(interval, DefaultConfig().IntervalSeconds) + 25
}

func (e *Engine) cacheSnapshot(a Agent, snapshot envelope.MonitorSnapshot, c Config, now time.Time) {
	seen := make(map[string]bool, len(snapshot.Containers))
	for _, m := range snapshot.Containers {
		if m.ID == "" {
			continue
		}
		if contains(c.ExcludedContainers, m.ID) || contains(c.ExcludedContainers, m.Name) {
			e.metrics.removeContainer(a.ID, m.ID)
			continue
		}
		seen[m.ID] = true
		e.metrics.put(a.ID, m, now, metricStaleAfter(c.IntervalSeconds))
		e.metrics.annotate(a.ID, m.ID, a.Name, snapshot.Complete)
	}
	if snapshot.Complete && snapshot.Error == "" {
		e.metrics.retainAgentContainers(a.ID, seen)
	} else {
		e.metrics.markStale(a.ID, seen)
	}
}
func (m *metricCache) prune(before int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range m.values {
		if v.ReceivedAt < before {
			delete(m.values, k)
		}
	}
}
