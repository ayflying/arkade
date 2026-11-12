package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func monitorFixture(t *testing.T, serve func(http.ResponseWriter, *http.Request)) *MonitorCollector {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(serve))
	t.Cleanup(server.Close)
	c, err := newClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return (&engine{client: c}).NewMonitorCollector()
}

const monitorInspectFixture = `{"Id":"full-id","Name":"/web","Config":{"Image":"nginx","Env":["PASSWORD=secret"]},"RestartCount":3,"State":{"Status":"running","StartedAt":"2024-01-01T00:00:00Z","FinishedAt":"0001-01-01T00:00:00Z","Health":{"Status":"healthy"}}}`
const monitorStatsFixture = `{"cpu_stats":{"cpu_usage":{"total_usage":500},"system_cpu_usage":2000,"online_cpus":4},"precpu_stats":{"cpu_usage":{"total_usage":100},"system_cpu_usage":1000},"memory_stats":{"usage":800,"limit":2000,"stats":{"inactive_file":200}},"networks":{"eth0":{"rx_bytes":123,"tx_bytes":456},"eth1":{"rx_bytes":10,"tx_bytes":20}},"blkio_stats":{"io_service_bytes_recursive":[{"op":"Read","value":70},{"op":"Write","value":80},{"op":"Total","value":150}]}}`

func TestMonitorHTTPFormula(t *testing.T) {
	m := monitorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("monitor must be read-only")
		}
		switch r.URL.Path {
		case pathContainers:
			if r.URL.Query().Get("all") != "1" {
				t.Error("must list all")
			}
			fmt.Fprint(w, `[{"Id":"full-id","Names":["/web"],"State":"running"}]`)
		case "/containers/full-id/json":
			fmt.Fprint(w, monitorInspectFixture)
		case "/containers/full-id/stats":
			if r.URL.Query().Get("stream") != "false" || r.URL.Query().Get("one-shot") != "true" {
				t.Error("missing bounded stats flags")
			}
			fmt.Fprint(w, monitorStatsFixture)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	s := m.Snapshot(context.Background())
	if !s.Complete || len(s.Containers) != 1 {
		t.Fatalf("%+v", s)
	}
	c := s.Containers[0]
	if !c.StatsAvailable || c.CPUPercent != 160 || c.MemoryBytes != 600 || c.MemoryPercent != 30 || c.NetworkRXBytes != 133 || c.NetworkTXBytes != 476 || c.BlockReadBytes != 70 || c.BlockWriteBytes != 80 || c.Health != "healthy" || c.RestartCount != 3 || c.StartedAt != 1704067200 || c.FinishedAt != 0 {
		t.Fatalf("wrong metrics: %+v", c)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "PASSWORD") {
		t.Fatal("inspect leaked")
	}
}
func TestMonitorCPUCacheAndMissingFields(t *testing.T) {
	var calls atomic.Int32
	m := monitorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == pathContainers:
			fmt.Fprint(w, `[{"Id":"full-id"}]`)
		case strings.HasSuffix(r.URL.Path, "/json"):
			fmt.Fprint(w, monitorInspectFixture)
		default:
			n := calls.Add(1)
			fmt.Fprintf(w, `{"cpu_stats":{"cpu_usage":{"total_usage":%d},"system_cpu_usage":%d,"online_cpus":2},"memory_stats":{"usage":10,"limit":100},"networks":{},"blkio_stats":{"io_service_bytes_recursive":[]}}`, n*100, n*1000)
		}
	})
	first := m.Snapshot(context.Background())
	if first.Containers[0].StatsAvailable || !strings.Contains(first.Containers[0].Error, "baseline") {
		t.Fatalf("first sample must be unavailable: %+v", first)
	}
	second := m.Snapshot(context.Background())
	if !second.Containers[0].StatsAvailable || second.Containers[0].CPUPercent != 20 {
		t.Fatalf("cache formula: %+v", second)
	}
}
func TestMonitorUnavailableSanitized(t *testing.T) {
	for _, body := range []string{`{"message":"Env PASSWORD=secret"}`, `{broken secret`, strings.Repeat("x", monitorBodyLimit+1), `{}`} {
		t.Run(fmt.Sprintf("length-%d", len(body)), func(t *testing.T) {
			m := monitorFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == pathContainers:
					fmt.Fprint(w, `[{"Id":"full-id"}]`)
				case strings.HasSuffix(r.URL.Path, "/json"):
					fmt.Fprint(w, monitorInspectFixture)
				default:
					if strings.Contains(body, "message") {
						w.WriteHeader(500)
					}
					fmt.Fprint(w, body)
				}
			})
			s := m.Snapshot(context.Background())
			if !s.Complete || s.Containers[0].StatsAvailable || !strings.Contains(s.Containers[0].Error, "unavailable") || strings.Contains(s.Containers[0].Error, "secret") {
				t.Fatalf("unsafe unavailable: %+v", s)
			}
		})
	}
}
func TestMonitorCancellation(t *testing.T) {
	started := make(chan struct{})
	m := monitorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathContainers {
			fmt.Fprint(w, `[{"Id":"full-id"}]`)
			return
		}
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan MonitorSnapshot, 1)
	go func() { done <- m.Snapshot(ctx) }()
	<-started
	cancel()
	select {
	case s := <-done:
		if s.Complete {
			t.Fatal("canceled snapshot complete")
		}
	case <-time.After(time.Second):
		t.Fatal("request did not cancel")
	}
}
func TestMonitorWorkerAndContainerLimits(t *testing.T) {
	var current, peak atomic.Int32
	m := monitorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathContainers {
			list := make([]map[string]string, 2001)
			for i := range list {
				list[i] = map[string]string{"Id": fmt.Sprintf("id-%d", i)}
			}
			json.NewEncoder(w).Encode(list)
			return
		}
		n := current.Add(1)
		defer current.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		// 短暂等待让并发 worker 重叠，验证实际 HTTP 并发上限。
		time.Sleep(time.Millisecond)
		fmt.Fprint(w, `{"State":{"Status":"exited"}}`)
	})
	s := m.Snapshot(context.Background())
	if s.Complete || len(s.Containers) != 2000 || !strings.Contains(s.Error, "2000") || peak.Load() > 4 {
		t.Fatalf("limits broken: complete=%v len=%d error=%s peak=%d", s.Complete, len(s.Containers), s.Error, peak.Load())
	}
}
