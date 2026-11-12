package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMonitorWireContract(t *testing.T) {
	raw, err := json.Marshal(MonitorSnapshot{Containers: []ContainerMetric{{ID: "full-id", CPUPercent: 250, NetworkRXBytes: 123}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"at", "containers", "complete", "error", "id", "name", "image", "state", "health", "exit_code", "restart_count", "started_at", "finished_at", "cpu_percent", "memory_bytes", "memory_limit_bytes", "memory_percent", "network_rx_bytes", "network_tx_bytes", "block_read_bytes", "block_write_bytes", "stats_available"} {
		if !strings.Contains(string(raw), `"`+key+`":`) {
			t.Errorf("missing wire key %s: %s", key, raw)
		}
	}
	if MethodContainerMonitorSnapshot != "container.monitor.snapshot" {
		t.Fatal("method changed")
	}
}
