package link

import (
	"context"
	"errors"
	"testing"

	"arkade/agent/internal/runtime"
	"arkade/protocol/envelope"
)

type failingHeartbeatRuntime struct{ testRuntime }

func (failingHeartbeatRuntime) List(context.Context, bool) ([]runtime.Container, error) {
	return nil, errors.New("engine unavailable")
}

func TestHeartbeatRuntimeStatusIndependentOfHostMetrics(t *testing.T) {
	for _, tc := range []struct {
		name    string
		rt      runtime.Runtime
		ok      bool
		wantErr bool
		count   int
	}{
		{"available", testRuntime{}, true, false, 1},
		{"list failed", failingHeartbeatRuntime{}, false, true, 0},
		{"no runtime", runtime.NewNone(""), false, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics := &envelope.HostMetrics{Memory: &envelope.ResourceUsage{UsedBytes: 1, TotalBytes: 2}}
			a := &Agent{opts: Options{Runtime: tc.rt}, sampleHostMetrics: func() *envelope.HostMetrics { return metrics }}
			hb, err := a.buildHeartbeat(context.Background())
			if (err != nil) != tc.wantErr || hb.HostMetrics != metrics || hb.RuntimeOK == nil || *hb.RuntimeOK != tc.ok || hb.ContainerTotal != tc.count || hb.ContainerRunning != tc.count {
				t.Fatalf("heartbeat = %+v, err = %v", hb, err)
			}
		})
	}
}
