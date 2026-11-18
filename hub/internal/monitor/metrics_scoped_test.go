package monitor

import (
	"testing"
	"time"

	"arkade/protocol/envelope"
)

func TestScopedMetricsRemainAvailableWithoutAlerts(t *testing.T) {
	e, source, c, now, events := fixture(t)
	c.Enabled = false
	c.IntervalSeconds = 10
	c.DurationSeconds = 0
	source.snapshot.Containers[0].NetworkAvailable = new(bool)
	*source.snapshot.Containers[0].NetworkAvailable = true
	e.Suppress("a", "c", time.Minute)
	tick(t, e)
	rows, after := e.ContainerMetrics("a")
	if len(rows) != 1 || after != 45 || rows[0].Stale(now.Unix(), after) || !rows[0].StatsAvailable || len(*events) != 0 || len(e.states) != 0 {
		t.Fatalf("scoped samples coupled to alerting: %+v, after %d", rows, after)
	}
	if others, _ := e.ContainerMetrics("other"); len(others) != 0 {
		t.Fatal("cross-agent cache data")
	}
	*now = now.Add(10 * time.Second)
	source.snapshot.Containers[0].NetworkRXBytes = 100
	tick(t, e)
	rows, _ = e.ContainerMetrics("a")
	rx, tx, ok := rows[0].NetworkRates()
	if rx != 10 || tx != 0 || !ok {
		t.Fatalf("wrong rates: %v %v %v", rx, tx, ok)
	}
	*now = now.Add(10 * time.Second)
	source.snapshot.Error = "collection failed"
	source.snapshot.Containers[0].NetworkRXBytes = 500
	tick(t, e)
	rows, _ = e.ContainerMetrics("a")
	if !rows[0].Stale(now.Unix(), after) || rows[0].NetworkRXBytes != 100 {
		t.Fatalf("failed snapshot renewed cache: %+v", rows[0])
	}
	source.snapshot.Error = ""
	*now = now.Add(10 * time.Second)
	tick(t, e)
	rows, _ = e.ContainerMetrics("a")
	if _, _, ok := rows[0].NetworkRates(); ok || rows[0].Stale(now.Unix(), after) || !rows[0].Stale(now.Unix()+after, after) {
		t.Fatalf("rates bridged collection gap or wrong freshness: %+v", rows[0])
	}
	c.ExcludedContainers = []string{"renamed"}
	source.snapshot.Containers[0].Name = "renamed"
	source.snapshot.Complete = false
	tick(t, e)
	if rows, _ := e.ContainerMetrics("a"); len(rows) != 0 {
		t.Fatal("partial snapshot retained newly excluded container")
	}
}

func TestScopedNetworkRatesRequireSafeFreshConsistentSamples(t *testing.T) {
	at := time.Unix(100, 0)
	safe := true
	baseline := envelope.ContainerMetric{ID: "c", NetworkAvailable: &safe, NetworkRXBytes: 100, StartedAt: 50}
	for _, tc := range []struct {
		name   string
		gap    int64
		change func(*envelope.ContainerMetric)
	}{
		{"old agent", 10, func(m *envelope.ContainerMetric) { m.NetworkAvailable = nil }},
		{"reset", 10, func(m *envelope.ContainerMetric) { m.NetworkRXBytes = 0 }},
		{"restart", 10, func(m *envelope.ContainerMetric) { m.StartedAt++ }},
		{"expired baseline", 86, func(*envelope.ContainerMetric) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cache metricCache
			cache.put("a", baseline, at, 85)
			next := baseline
			next.NetworkRXBytes += 100
			tc.change(&next)
			cache.put("a", next, at.Add(time.Duration(tc.gap)*time.Second), 85)
			if _, _, ok := cache.agentMetrics("a")[0].NetworkRates(); ok {
				t.Fatal("unsafe or invalid baseline produced rate")
			}
		})
	}
}
