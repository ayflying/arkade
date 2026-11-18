package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	api "arkade/hub/api/resources/v1"
	"arkade/hub/internal/auth"
	resources "arkade/hub/internal/controller/resources"
	"arkade/hub/internal/monitor"
	"arkade/protocol/envelope"
	"github.com/gogf/gf/v2/net/ghttp"
)

func TestContainerMetricsRouteRequiresSession(t *testing.T) {
	s := ghttp.GetServer(uniqueServerName())
	s.SetDumpRouterMap(false)
	bindNativeModule(s, Deps{Auth: auth.NewService(nil, 0)}, resources.New(resources.Deps{}))
	base := startRouterForTest(t, s)
	resp, body := doHTTP(t, http.MethodGet, base+"/api/agents/a/container-metrics", "", nil)
	if resp.StatusCode != 401 || !strings.Contains(string(body), `"unauthorized"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

type containerMetricSource struct{ calls atomic.Int32 }

func (*containerMetricSource) Agents(context.Context) ([]monitor.Agent, error) {
	// They have cached samples, but are disconnected from the actual HTTP registry.
	return []monitor.Agent{{ID: "metrics-allowed", Online: true}, {ID: "metrics-denied", Online: true}}, nil
}

func (s *containerMetricSource) Snapshot(_ context.Context, id string) (envelope.MonitorSnapshot, error) {
	s.calls.Add(1)
	safe := true
	return envelope.MonitorSnapshot{Complete: true, Containers: []envelope.ContainerMetric{
		{ID: id + "-1", StatsAvailable: true, MemoryLimitBytes: 1024, NetworkAvailable: &safe, Error: "private data"},
		{ID: id + "-2", NetworkRXBytes: 99}, // Old agent: no network attribution flag.
	}}, nil
}

func TestContainerMetricsScopedHTTP(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.seedAgent(t, "metrics-allowed")
	e.seedAgent(t, "metrics-denied")
	uid := e.createUser(t, admin, "metricsviewer", "ViewerPass123", "user")
	group := e.createGroup(t, admin, "metrics-readers")
	for _, r := range []struct{ path, body string }{
		{"/members", `{"user_ids":["` + uid + `"]}`},
		{"/grants", `{"grants":[{"agent_id":"metrics-allowed","permission":"view"}]}`},
	} {
		rec, _ := e.do(t, http.MethodPut, "/api/groups/"+group.ID+r.path, r.body, admin)
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
	}
	source := &containerMetricSource{}
	engine := monitor.NewEngine(nil, source, func(context.Context) (monitor.Config, error) { return monitor.DefaultConfig(), nil }, nil, nil)
	if err := engine.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(Deps{Log: e.log, DB: e.db, Auth: e.auth, Registry: e.registry, MonitorEngine: engine, Identity: testIdentityServices(t, e.db, e.auth, e.log)})
	base := startRouterForTest(t, router)
	viewer := e.loginAsFor(t, base, "metricsviewer", "ViewerPass123")
	path := base + "/api/agents/"
	resp, body := doHTTP(t, http.MethodGet, path+"metrics-allowed/container-metrics?agentID=metrics-denied", "", viewer)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("%d %s %s", resp.StatusCode, resp.Header.Get("Cache-Control"), body)
	}
	var got api.ContainerMetricsRes
	if err := json.Unmarshal(body, &got); err != nil || got.AgentID != "metrics-allowed" || got.StaleAfterSeconds != 85 || len(got.Containers) != 2 {
		t.Fatalf("bad contract: %s (%v)", body, err)
	}
	first, old := got.Containers[0], got.Containers[1]
	if first.ContainerID != "metrics-allowed-1" || first.At <= 0 || !first.Stale || !first.StatsAvailable || first.CPUPercent == nil || *first.CPUPercent != 0 || !first.NetworkAvailable || first.NetworkRXBytes == nil || *first.NetworkRXBytes != 0 || first.RatesAvailable || first.NetworkRXPerSecond != nil {
		t.Fatalf("zero, freshness or availability lost: %s", body)
	}
	if old.NetworkAvailable || old.NetworkRXBytes != nil || old.CPUPercent != nil || strings.Contains(string(body), "private data") {
		t.Fatalf("unexpected fields: %s", body)
	}
	for _, tc := range []struct {
		agent   string
		cookies []*http.Cookie
		code    int
	}{
		{"metrics-allowed", nil, 401}, {"metrics-denied", viewer, 403}, {"missing", viewer, 404},
	} {
		resp, body := doHTTP(t, http.MethodGet, path+tc.agent+"/container-metrics", "", tc.cookies)
		if resp.StatusCode != tc.code {
			t.Fatalf("%s: %d %s", tc.agent, resp.StatusCode, body)
		}
	}
	if got := source.calls.Load(); got != 2 {
		t.Fatalf("request triggered sampling: %d calls, want 2 from setup", got)
	}
}
