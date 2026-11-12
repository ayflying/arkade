package monitor

import (
	"context"
	"testing"
	"time"

	"arkade/hub/internal/store"
	"arkade/hub/internal/testdb"
	"arkade/protocol/envelope"
)

// TestEnginePostgresRestartPersistence 验证真实持久化路径，而非仅靠引擎内存去重：
// 成功通知状态在重启后恢复，恢复通知也只发生一次，且全程使用同一事件记录。
func TestEnginePostgresRestartPersistence(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Unix(100000, 0)
	config := Config{Enabled: true, DurationSeconds: 0, RepeatSeconds: 1800, OfflineSeconds: 120, CPUPercent: 150, MemoryPercent: 80, RecoveryNotify: true, DingTalkEnabled: true}
	source := &fakeSource{agents: []Agent{{ID: "persist-agent", Online: true}}, snapshot: envelope.MonitorSnapshot{Complete: true, Containers: []envelope.ContainerMetric{{ID: "persist-container", State: "running", StatsAvailable: true, CPUPercent: 200, MemoryLimitBytes: 100, MemoryPercent: 10}}}}
	events := []Event{}
	newEngine := func() *Engine {
		e := NewEngine(db, source, func(context.Context) (Config, error) { return config, nil }, func(_ context.Context, event Event) error { events = append(events, event); return nil }, nil)
		e.now = func() time.Time { return now }
		return e
	}
	assertHistory := func(status string, deliveryCount int) store.MonitorIncident {
		t.Helper()
		incidents, err := db.ListMonitorIncidents(ctx, 200)
		if err != nil {
			t.Fatal(err)
		}
		if len(incidents) != 1 || incidents[0].Status != status {
			t.Fatalf("incident status want %s: %+v", status, incidents)
		}
		deliveries, err := db.ListMonitorDeliveries(ctx, 200)
		if err != nil {
			t.Fatal(err)
		}
		if len(deliveries) != deliveryCount {
			t.Fatalf("delivery count want %d: %+v", deliveryCount, deliveries)
		}
		for _, delivery := range deliveries {
			if delivery.IncidentID != incidents[0].ID || delivery.Channel != "dingtalk" || delivery.Status != "sent" {
				t.Fatalf("delivery metadata %+v", delivery)
			}
		}
		return incidents[0]
	}

	first := newEngine()
	tick(t, first)
	firing := assertHistory("firing", 1)
	if len(events) != 1 || events[0].Event != "firing" || firing.LastNotified != now.Unix() {
		t.Fatalf("initial firing %+v events %+v", firing, events)
	}
	if firing.DetailJSON == "" {
		t.Fatal("engine state was not persisted")
	}

	// 重建引擎丢弃全部内存状态，只允许从 PG 恢复成功通知标记。
	now = now.Add(time.Minute)
	restarted := newEngine()
	tick(t, restarted)
	afterRestart := assertHistory("firing", 1)
	if len(events) != 1 || afterRestart.ID != firing.ID || afterRestart.FirstSeen != firing.FirstSeen || afterRestart.LastNotified != firing.LastNotified {
		t.Fatalf("restart duplicated or reset firing: %+v events %+v", afterRestart, events)
	}

	source.snapshot.Containers[0].CPUPercent = 10
	now = now.Add(time.Minute)
	tick(t, restarted)
	resolved := assertHistory("resolved", 2)
	if len(events) != 2 || events[1].Event != "resolved" || resolved.ID != firing.ID || resolved.ResolvedAt != now.Unix() {
		t.Fatalf("recovery %+v events %+v", resolved, events)
	}
	deliveries, err := db.ListMonitorDeliveries(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	if deliveries[0].Event != "resolved" || deliveries[1].Event != "firing" {
		t.Fatalf("delivery lifecycle %+v", deliveries)
	}

	now = now.Add(time.Minute)
	secondRestart := newEngine()
	tick(t, secondRestart)
	tick(t, secondRestart)
	afterRecoveryRestart := assertHistory("resolved", 2)
	if len(events) != 2 || afterRecoveryRestart.ResolvedAt != resolved.ResolvedAt {
		t.Fatalf("restart repeated recovery %+v events %+v", afterRecoveryRestart, events)
	}
	open, err := db.ListOpenMonitorIncidents(ctx)
	if err != nil || len(open) != 0 {
		t.Fatalf("resolved incident restored as open: %+v %v", open, err)
	}
}
