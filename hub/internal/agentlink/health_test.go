package agentlink

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

func TestHealthExpiryAndPartialMetrics(t *testing.T) {
	var sample healthSample
	now := time.Now()
	cpu, runtimeOK := 0.0, false
	sample.record(&runtimeOK, &envelope.HostMetrics{
		CPUPercent: &cpu,
		Memory:     &envelope.ResourceUsage{UsedBytes: 3, TotalBytes: 2},
	}, now)
	got := sample.current(now.Add(heartbeatTimeout - time.Second))
	if got == nil || got.RuntimeOK == nil || *got.RuntimeOK || got.SampledAt != now.Unix() ||
		got.HostMetrics == nil || got.HostMetrics.CPUPercent == nil || *got.HostMetrics.CPUPercent != 0 || got.HostMetrics.Memory != nil {
		t.Fatalf("unexpected sample: %+v", got)
	}
	if sample.current(now.Add(heartbeatTimeout)) != nil {
		t.Fatal("expired sample presented as current")
	}
	sample.record(nil, nil, now)
	if sample.current(now) != nil {
		t.Fatal("old-format heartbeat must not turn missing values into zeros")
	}
}

func TestHealthBoundToActiveConnection(t *testing.T) {
	r := NewRegistry()
	old := newConnection("remote", nil, slog.Default())
	r.Add(old)
	ok := true
	old.health.record(&ok, nil, time.Now())
	if r.Health("remote") == nil {
		t.Fatal("expected registered connection sample")
	}
	r.Remove(old)
	current := newConnection("remote", nil, slog.Default())
	r.Add(current)
	old.health.record(&ok, nil, time.Now())
	if r.Health("remote") != nil {
		t.Fatal("new connection must not expose old samples")
	}
	local := localFunc(func(context.Context, string, []byte) ([]byte, *envelope.Error) { return nil, nil })
	if err := r.RegisterLocal("local", local); err != nil {
		t.Fatal(err)
	}
	record := r.LocalHealthRecorder("local")
	record(true, nil)
	if r.Health("local") == nil {
		t.Fatal("expected local sample")
	}
	r.RemoveLocal("local")
	if err := r.RegisterLocal("local", local); err != nil {
		t.Fatal(err)
	}
	record(false, nil)
	if r.Health("local") != nil {
		t.Fatal("new executor must not expose old samples")
	}
	r.RemoveLocal("local")
}
