package monitor

import (
	"context"
	"errors"
	"testing"
	"time"

	"arkade/protocol/envelope"
)

type fakeSource struct {
	agents   []Agent
	snapshot envelope.MonitorSnapshot
	err      error
	calls    int
}

func (f *fakeSource) Agents(context.Context) ([]Agent, error) { return f.agents, nil }
func (f *fakeSource) Snapshot(context.Context, string) (envelope.MonitorSnapshot, error) {
	f.calls++
	return f.snapshot, f.err
}
func fixture(t *testing.T) (*Engine, *fakeSource, *Config, *time.Time, *[]Event) {
	t.Helper()
	now := time.Unix(100000, 0)
	c := Config{Enabled: true, DurationSeconds: 120, RepeatSeconds: 1800, OfflineSeconds: 120, CPUPercent: 150, MemoryPercent: 80, RecoveryNotify: true, DingTalkEnabled: true}
	source := &fakeSource{agents: []Agent{{ID: "a", Online: true}}, snapshot: envelope.MonitorSnapshot{Complete: true, Containers: []envelope.ContainerMetric{{ID: "c", State: "running", StatsAvailable: true, CPUPercent: 200, MemoryLimitBytes: 100, MemoryPercent: 10}}}}
	events := []Event{}
	e := NewEngine(nil, source, func(context.Context) (Config, error) { return c, nil }, func(_ context.Context, v Event) error { events = append(events, v); return nil }, nil)
	e.now = func() time.Time { return now }
	return e, source, &c, &now, &events
}
func tick(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestEngineDurationRepeatRecovery(t *testing.T) {
	e, s, _, now, events := fixture(t)
	tick(t, e)
	*now = now.Add(119 * time.Second)
	tick(t, e)
	if len(*events) != 0 {
		t.Fatal("early firing")
	}
	*now = now.Add(time.Second)
	tick(t, e)
	tick(t, e)
	if len(*events) != 1 || (*events)[0].Event != "firing" {
		t.Fatal(*events)
	}
	*now = now.Add(1800 * time.Second)
	tick(t, e)
	if len(*events) != 2 || (*events)[1].Event != "repeat" {
		t.Fatal(*events)
	}
	s.snapshot.Containers[0].CPUPercent = 10
	tick(t, e)
	tick(t, e)
	if len(*events) != 3 || (*events)[2].Event != "resolved" {
		t.Fatal(*events)
	}
}
func TestMissingResourcesDoNotRecover(t *testing.T) {
	e, s, _, now, events := fixture(t)
	tick(t, e)
	*now = now.Add(120 * time.Second)
	tick(t, e)
	s.snapshot.Containers[0].StatsAvailable = false
	s.snapshot.Containers[0].CPUPercent = 0
	tick(t, e)
	if len(*events) != 1 {
		t.Fatal(*events)
	}
	s.snapshot.Containers[0].StatsAvailable = true
	tick(t, e)
	if len(*events) != 2 {
		t.Fatal(*events)
	}
}
func TestMissingResourcesResetPending(t *testing.T) {
	e, s, _, now, events := fixture(t)
	tick(t, e)
	*now = now.Add(100 * time.Second)
	s.snapshot.Containers[0].StatsAvailable = false
	tick(t, e)
	*now = now.Add(100 * time.Second)
	s.snapshot.Containers[0].StatsAvailable = true
	tick(t, e)
	if len(*events) != 0 {
		t.Fatal("missing sample counted toward sustained duration")
	}
	*now = now.Add(120 * time.Second)
	tick(t, e)
	if len(*events) != 1 {
		t.Fatal(*events)
	}
}
func TestDisableAndSuppressNeverRecover(t *testing.T) {
	e, _, c, now, events := fixture(t)
	tick(t, e)
	*now = now.Add(120 * time.Second)
	tick(t, e)
	c.Enabled = false
	tick(t, e)
	if len(*events) != 1 || len(e.states) != 0 {
		t.Fatal(*events)
	}
	c.Enabled = true
	tick(t, e)
	*now = now.Add(120 * time.Second)
	tick(t, e)
	e.Suppress("a", "c", time.Minute)
	tick(t, e)
	if len(*events) != 2 || len(e.states) != 0 {
		t.Fatal(*events)
	}
}
func TestOfflineUsesHubTimeAndNoContainerStorm(t *testing.T) {
	e, s, _, now, events := fixture(t)
	s.snapshot.Containers[0].CPUPercent = 0
	tick(t, e)
	s.agents[0].Online = false
	s.agents[0].LastSeen = 1
	*now = now.Add(119 * time.Second)
	tick(t, e)
	if len(*events) != 0 {
		t.Fatal("trusted agent timestamp")
	}
	*now = now.Add(time.Second)
	tick(t, e)
	if len(*events) != 1 || (*events)[0].Incident.Kind != "offline" {
		t.Fatal(*events)
	}
	if s.calls != 1 {
		t.Fatal("collected offline agent")
	}
}
func TestInitiallyStoppedAndCleanExitIgnored(t *testing.T) {
	e, s, _, now, events := fixture(t)
	m := &s.snapshot.Containers[0]
	m.State = "exited"
	m.ExitCode = 1
	m.CPUPercent = 0
	tick(t, e)
	*now = now.Add(120 * time.Second)
	tick(t, e)
	if len(*events) != 0 {
		t.Fatal(*events)
	}
	m.State = "running"
	tick(t, e)
	m.State = "exited"
	m.ExitCode = 0
	tick(t, e)
	*now = now.Add(120 * time.Second)
	tick(t, e)
	if len(*events) != 0 {
		t.Fatal(*events)
	}
	m.ExitCode = 1
	tick(t, e)
	*now = now.Add(120 * time.Second)
	tick(t, e)
	if len(*events) != 1 || (*events)[0].Incident.Kind != "unexpected_exit" {
		t.Fatal(*events)
	}
}
func TestDeliveryRetryBackoffAndNoUnsentRecovery(t *testing.T) {
	e, s, _, now, _ := fixture(t)
	calls := 0
	e.notify = func(context.Context, Event) error { calls++; return errors.New("fake failure") }
	tick(t, e)
	*now = now.Add(120 * time.Second)
	tick(t, e)
	tick(t, e)
	if calls != 1 {
		t.Fatal("tight retry", calls)
	}
	*now = now.Add(30 * time.Second)
	tick(t, e)
	if calls != 2 {
		t.Fatal(calls)
	}
	s.snapshot.Containers[0].CPUPercent = 0
	tick(t, e)
	if calls != 2 {
		t.Fatal("recovery without sent alert")
	}
}
func TestCollectionFailureSeparateAndNotRecovery(t *testing.T) {
	e, s, _, now, events := fixture(t)
	tick(t, e)
	*now = now.Add(120 * time.Second)
	tick(t, e)
	s.err = errors.New("offline stats")
	tick(t, e)
	*now = now.Add(120 * time.Second)
	tick(t, e)
	if len(*events) != 2 || (*events)[1].Incident.Kind != "collection" {
		t.Fatal(*events)
	}
}
func TestSlowNotificationsDoNotBlockEvaluationAndRotate(t *testing.T) {
	e, s, c, now, _ := fixture(t)
	c.DurationSeconds = 0
	c.EmailEnabled = true
	e.deliveryBudget = 20 * time.Millisecond
	s.agents = []Agent{{ID: "a", Online: true}, {ID: "b", Online: true}, {ID: "c", Online: false}}
	e.offlineSince["c"] = now.Unix() - 120
	attempted := []string{}
	e.notify = func(ctx context.Context, event Event) error {
		// 即使第一个通知阻塞，所有采样与失联规则必须已评估并进入 firing。
		for _, rule := range []string{ruleKey("a", "c", "cpu"), ruleKey("b", "c", "cpu"), ruleKey("c", "", "offline")} {
			if state := e.states[rule]; state == nil || state.Incident.Status != "firing" {
				t.Errorf("notification preceded evaluation: %q", rule)
			}
		}
		attempted = append(attempted, event.Incident.AgentID)
		<-ctx.Done()
		return ctx.Err()
	}
	tick(t, e)
	*now = now.Add(30 * time.Second)
	tick(t, e)
	*now = now.Add(30 * time.Second)
	tick(t, e)
	seen := map[string]bool{}
	for _, agent := range attempted {
		seen[agent] = true
	}
	if !seen["a"] || !seen["b"] || !seen["c"] {
		t.Fatal("delivery starvation", attempted)
	}
	if len(e.Metrics()) != 2 {
		t.Fatal("later agent metrics not evaluated")
	}
}

func TestContainerWildcardDoesNotSuppressAgentOffline(t *testing.T) {
	e, s, c, now, events := fixture(t)
	c.DurationSeconds = 0
	s.snapshot.Containers[0].CPUPercent = 0
	tick(t, e)
	e.Suppress("a", "*", time.Hour)
	s.agents[0].Online = false
	*now = now.Add(120 * time.Second)
	tick(t, e)
	if len(*events) != 1 || (*events)[0].Incident.Kind != "offline" {
		t.Fatal(*events)
	}
	tick(t, e)
	if e.states[ruleKey("a", "", "offline")] == nil {
		t.Fatal("wildcard closed offline incident")
	}
}
func TestNameExclusionClosesSilentlyEvenWhenOffline(t *testing.T) {
	e, s, c, _, events := fixture(t)
	c.DurationSeconds = 0
	s.snapshot.Containers[0].Name = "excluded-name"
	tick(t, e)
	c.ExcludedContainers = []string{"excluded-name"}
	s.agents[0].Online = false
	tick(t, e)
	if len(*events) != 1 || len(e.states) != 0 || len(e.Metrics()) != 0 {
		t.Fatal("name exclusion did not clear prior state silently")
	}
}
func TestCompleteInventoryRemovesMissingMetricsOnly(t *testing.T) {
	e, s, _, _, _ := fixture(t)
	tick(t, e)
	s.snapshot.Containers = nil
	s.snapshot.Complete = false
	tick(t, e)
	if len(e.Metrics()) != 1 {
		t.Fatal("partial inventory removed metric")
	}
	s.snapshot.Complete = true
	tick(t, e)
	if len(e.Metrics()) != 0 {
		t.Fatal("complete inventory kept removed metric")
	}
}

func TestZeroDurationAndShortIDSuppression(t *testing.T) {
	e, s, c, _, events := fixture(t)
	c.DurationSeconds = 0
	s.snapshot.Containers[0].ID = "abcdef1234567890"
	tick(t, e)
	if len(*events) != 1 {
		t.Fatal("zero duration did not fire")
	}
	e.Suppress("a", "abcdef123456", time.Minute)
	tick(t, e)
	if len(*events) != 1 || len(e.states) != 0 {
		t.Fatal("short ID suppression emitted recovery")
	}
}
func TestStoppedMissingStatsIsNotCollectionFailure(t *testing.T) {
	e, s, c, _, events := fixture(t)
	c.DurationSeconds = 0
	m := &s.snapshot.Containers[0]
	m.State = "exited"
	m.StatsAvailable = false
	m.Error = "stats unavailable"
	tick(t, e)
	if len(*events) != 0 {
		t.Fatal(*events)
	}
}
func TestChannelFailureDoesNotResendSuccessfulChannel(t *testing.T) {
	e, _, c, now, _ := fixture(t)
	c.DurationSeconds = 0
	c.EmailEnabled = true
	calls := map[string]int{}
	e.notify = func(_ context.Context, event Event) error {
		calls[event.Channel]++
		if event.Channel == "email" {
			return errors.New("failure")
		}
		return nil
	}
	tick(t, e)
	*now = now.Add(30 * time.Second)
	tick(t, e)
	if calls["dingtalk"] != 1 || calls["email"] != 2 {
		t.Fatal(calls)
	}
}
func TestRatesResetAndAgentTimestampIgnored(t *testing.T) {
	e, s, _, now, _ := fixture(t)
	s.snapshot.At = 999999999999
	s.snapshot.Containers[0].NetworkRXBytes = 100
	tick(t, e)
	*now = now.Add(10 * time.Second)
	s.snapshot.Containers[0].NetworkRXBytes = 200
	tick(t, e)
	m := e.Metrics()[0]
	if !m.RatesAvailable || m.NetworkRXPerSecond != 10 || m.ReceivedAt != now.Unix() {
		t.Fatal(m)
	}
	*now = now.Add(10 * time.Second)
	s.snapshot.Containers[0].NetworkRXBytes = 1
	tick(t, e)
	if e.Metrics()[0].RatesAvailable {
		t.Fatal("counter reset produced rate")
	}
}
