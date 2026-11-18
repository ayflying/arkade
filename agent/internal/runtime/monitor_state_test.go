package runtime

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMonitorStoppedContainerStillComplete(t *testing.T) {
	m := monitorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == pathContainers:
			fmt.Fprint(w, `[{"Id":"stopped-id","State":"exited"}]`)
		case strings.HasSuffix(r.URL.Path, "/json"):
			fmt.Fprint(w, `{"Id":"stopped-id","State":{"Status":"exited","ExitCode":137,"FinishedAt":"2024-01-02T00:00:00Z"}}`)
		default:
			t.Error("stopped container must not request stats")
		}
	})
	s := m.Snapshot(context.Background())
	if !s.Complete || s.Error != "" || s.Containers[0].StatsAvailable || s.Containers[0].ExitCode != 137 || s.Containers[0].Health != "" || s.Containers[0].FinishedAt != 1704153600 {
		t.Fatalf("stopped status lost: %+v", s)
	}
}
func TestMonitorInspectFailureIncomplete(t *testing.T) {
	m := monitorFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == pathContainers {
			fmt.Fprint(w, `[{"Id":"gone-id","State":"running"}]`)
			return
		}
		w.WriteHeader(404)
		fmt.Fprint(w, `{"message":"PASSWORD=secret"}`)
	})
	s := m.Snapshot(context.Background())
	if s.Complete || len(s.Containers) != 1 || s.Containers[0].StatsAvailable || strings.Contains(s.Containers[0].Error, "secret") {
		t.Fatalf("unsafe incomplete response: %+v", s)
	}
}
func TestMonitorCPUResetAndAccountingMissing(t *testing.T) {
	for name, stats := range map[string]string{
		"cpu-reset":       strings.Replace(monitorStatsFixture, `"total_usage":500`, `"total_usage":50`, 1),
		"memory-missing":  strings.Replace(monitorStatsFixture, `"usage":800,`, "", 1),
		"network-missing": strings.Replace(monitorStatsFixture, `"networks":`, `"unsupported_networks":`, 1),
		"block-missing":   strings.Replace(monitorStatsFixture, `"blkio_stats":`, `"unsupported_blkio":`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			m := monitorFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == pathContainers:
					fmt.Fprint(w, `[{"Id":"full-id"}]`)
				case strings.HasSuffix(r.URL.Path, "/json"):
					fmt.Fprint(w, monitorInspectFixture)
				default:
					fmt.Fprint(w, stats)
				}
			})
			s := m.Snapshot(context.Background())
			c := s.Containers[0]
			wantStats := name != "cpu-reset" && name != "memory-missing"
			if !s.Complete || c.StatsAvailable != wantStats || (c.Error == "") != wantStats ||
				c.NetworkAvailable != (name != "network-missing") || c.BlockAvailable != (name != "block-missing") {
				t.Fatalf("accounting availability not independent: %+v", s)
			}
		})
	}
}
