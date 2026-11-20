package runtime

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestMonitorNetworkAttribution(t *testing.T) {
	for _, tc := range []struct {
		name, mode, joiner, stats string
		available                 bool
	}{
		{"private bridge", "bridge", "", monitorStatsFixture, true},
		{"named network", "project_default", "", monitorStatsFixture, true},
		{"host", "host", "", monitorStatsFixture, false},
		{"unknown", "", "", monitorStatsFixture, false},
		{"joined namespace", "container:another", "", monitorStatsFixture, false},
		{"shared owner by id", "bridge", "container:full-id", monitorStatsFixture, false},
		{"shared owner by name", "bridge", "container:web", monitorStatsFixture, false},
		{"partial counters", "bridge", "", strings.Replace(monitorStatsFixture, `,"tx_bytes":456`, "", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := monitorFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case pathContainers:
					if tc.joiner != "" {
						fmt.Fprint(w, `[{"Id":"full-id"},{"Id":"joiner"}]`)
					} else {
						fmt.Fprint(w, `[{"Id":"full-id"}]`)
					}
				case "/containers/full-id/json":
					fmt.Fprint(w, strings.Replace(monitorInspectFixture, `"NetworkMode":"bridge"`, fmt.Sprintf(`"NetworkMode":%q`, tc.mode), 1))
				case "/containers/joiner/json":
					fmt.Fprintf(w, `{"Id":"joiner","HostConfig":{"NetworkMode":%q},"State":{"Status":"running"}}`, tc.joiner)
				default:
					fmt.Fprint(w, tc.stats)
				}
			})
			s := m.Snapshot(context.Background(), nil)
			owner := s.Containers[0]
			if !s.Complete || !owner.StatsAvailable || owner.NetworkAvailable != tc.available || (!tc.available && (owner.NetworkRXBytes != 0 || owner.NetworkTXBytes != 0)) {
				t.Fatalf("unexpected owner metrics: %+v", owner)
			}
			if tc.joiner != "" && (s.Containers[1].NetworkAvailable || s.Containers[1].NetworkRXBytes != 0) {
				t.Fatalf("shared namespace counters leaked: %+v", s.Containers[1])
			}
		})
	}
}
