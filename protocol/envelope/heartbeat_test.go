package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHeartbeatTelemetryWireCompatibility(t *testing.T) {
	const old = `{"agent_id":"a","container_total":0,"container_running":0,"at":100}`
	var hb HeartbeatPayload
	if err := json.Unmarshal([]byte(old), &hb); err != nil || hb.RuntimeOK != nil || hb.HostMetrics != nil {
		t.Fatalf("old heartbeat = %+v, err = %v", hb, err)
	}
	wire, err := json.Marshal(hb)
	if err != nil || string(wire) != old {
		t.Fatalf("heartbeat with no telemetry = %s, err = %v", wire, err)
	}

	ok, zero := false, float64(0)
	hb.RuntimeOK = &ok
	hb.HostMetrics = &HostMetrics{CPUPercent: &zero, Disk: &DiskUsage{Scope: DiskScopeHostFilesystem, ResourceUsage: ResourceUsage{TotalBytes: 10}}}
	wire, err = json.Marshal(hb)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"runtime_ok":false`, `"cpu_percent":0`, `"disk":{"used_bytes":0,"total_bytes":10,"scope":"host_filesystem"}`} {
		if !strings.Contains(string(wire), field) {
			t.Errorf("missing %s in %s", field, wire)
		}
	}
	if strings.Contains(string(wire), `"memory"`) {
		t.Fatalf("missing memory should be omitted: %s", wire)
	}
}
