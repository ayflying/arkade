package httpx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	api "arkade/hub/api/resources/v1"
	"arkade/hub/internal/auth"
	resources "arkade/hub/internal/controller/resources"
	"arkade/hub/internal/store"
	"github.com/gogf/gf/v2/net/ghttp"
)

func TestContainerMetricHistoryRouteRequiresSession(t *testing.T) {
	s := ghttp.GetServer(uniqueServerName())
	s.SetDumpRouterMap(false)
	bindNativeModule(s, Deps{Auth: auth.NewService(nil, 0)}, resources.New(resources.Deps{}))
	base := startRouterForTest(t, s)
	resp, body := doHTTP(t, http.MethodGet, base+"/api/agents/a/containers/c/metrics/history?from=1&to=2", "", nil)
	if resp.StatusCode != 401 || !strings.Contains(string(body), `"unauthorized"`) {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
}

// seedHistory 写入两条位于最近一小时内的样本，返回它们的桶时间。
func seedHistory(t *testing.T, db *store.DB, agentID, containerID, project, name string) []int64 {
	t.Helper()
	now := time.Now().Unix()
	// 对齐到整分钟，桶起点稳定，便于断言
	base := now - now%60 - 300
	rows := []store.MonitorSample{
		{
			AgentID: agentID, ContainerID: containerID, Project: project, ContainerName: name,
			BucketAt: base, LastAt: base + 5, State: "running",
			StatsAvailable: true, CPUPercent: 12.5, MemoryBytes: 1024, MemoryLimitBytes: 4096, MemoryPercent: 25,
			NetworkAvailable: true, NetworkRXRate: 11, NetworkTXRate: 22,
			BlockAvailable: true, BlockReadRate: 33, BlockWriteRate: 44,
			RatesAvailable: true, SnapshotComplete: true,
		},
		{
			AgentID: agentID, ContainerID: containerID, Project: project, ContainerName: name,
			BucketAt: base + 60, LastAt: base + 65, State: "running",
			StatsAvailable: true, CPUPercent: 37.5, MemoryBytes: 2048, MemoryLimitBytes: 4096, MemoryPercent: 50,
			NetworkAvailable: true, NetworkRXRate: 55, NetworkTXRate: 66,
			BlockAvailable: true, BlockReadRate: 77, BlockWriteRate: 88,
			RatesAvailable: true, SnapshotComplete: true,
		},
	}
	if err := db.UpsertMonitorSamples(context.Background(), rows); err != nil {
		t.Fatal(err)
	}
	return []int64{base, base + 60}
}

func TestContainerMetricHistoryScopedHTTP(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.seedAgent(t, "hist-allowed")
	e.seedAgent(t, "hist-denied")
	uid := e.createUser(t, admin, "histviewer", "ViewerPass123", "user")
	group := e.createGroup(t, admin, "hist-readers")
	for _, r := range []struct{ path, body string }{
		{"/members", `{"user_ids":["` + uid + `"]}`},
		{"/grants", `{"grants":[{"agent_id":"hist-allowed","permission":"view"}]}`},
	} {
		rec, _ := e.do(t, http.MethodPut, "/api/groups/"+group.ID+r.path, r.body, admin)
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
	}
	buckets := seedHistory(t, e.db, "hist-allowed", "abc123", "proj", "web")
	router := NewRouter(Deps{Log: e.log, DB: e.db, Auth: e.auth, Registry: e.registry, Identity: testIdentityServices(t, e.db, e.auth, e.log)})
	base := startRouterForTest(t, router)
	viewer := e.loginAs(t, "histviewer", "ViewerPass123")

	now := time.Now().Unix()
	q := fmt.Sprintf("?from=%d&to=%d", now-3600, now)
	resp, body := doHTTP(t, http.MethodGet, base+"/api/agents/hist-allowed/containers/abc123/metrics/history"+q, "", viewer)
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	if !strings.Contains(resp.Header.Get("Cache-Control"), "no-store") {
		t.Fatalf("历史不允许被缓存: %q", resp.Header.Get("Cache-Control"))
	}
	var got api.ContainerMetricHistoryRes
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("解析失败: %v %s", err, body)
	}
	if got.RetentionDays != 7 || got.BucketSeconds != 60 {
		t.Fatalf("保留期与桶宽不对: %d %d", got.RetentionDays, got.BucketSeconds)
	}
	if len(got.Points) != 2 {
		t.Fatalf("应返回 2 个点，实际 %d: %s", len(got.Points), body)
	}
	if got.Points[0].At != buckets[0] || got.Points[1].At != buckets[1] {
		t.Fatalf("桶起点不对: %d %d", got.Points[0].At, got.Points[1].At)
	}
	if got.Points[0].CPUPercent == nil || *got.Points[0].CPUPercent != 12.5 {
		t.Fatalf("CPU 值丢了: %s", body)
	}
	if got.Points[1].BlockWritePerSecond == nil || *got.Points[1].BlockWritePerSecond != 88 {
		t.Fatalf("磁盘写入速率丢了: %s", body)
	}

	// 权限与参数错误
	for _, tc := range []struct {
		name string
		url  string
		code int
	}{
		{"无权服务器", base + "/api/agents/hist-denied/containers/abc123/metrics/history" + q, 403},
		{"服务器不存在", base + "/api/agents/missing/containers/abc123/metrics/history" + q, 404},
		{"缺少时间参数", base + "/api/agents/hist-allowed/containers/abc123/metrics/history", 400},
		{"超出保留期", fmt.Sprintf("%s/api/agents/hist-allowed/containers/abc123/metrics/history?from=%d&to=%d", base, now-9*86400, now-8*86400), 400},
		{"区间反向", fmt.Sprintf("%s/api/agents/hist-allowed/containers/abc123/metrics/history?from=%d&to=%d", base, now, now-600), 400},
	} {
		resp, body := doHTTP(t, http.MethodGet, tc.url, "", viewer)
		if resp.StatusCode != tc.code {
			t.Fatalf("%s: %d %s", tc.name, resp.StatusCode, body)
		}
	}

	// 超过 7 天的区间被夹紧，而不是报错
	long := fmt.Sprintf("%s/api/agents/hist-allowed/containers/abc123/metrics/history?from=%d&to=%d", base, now-30*86400, now)
	resp, body = doHTTP(t, http.MethodGet, long, "", viewer)
	if resp.StatusCode != 200 {
		t.Fatalf("夹紧失败: %d %s", resp.StatusCode, body)
	}
	var clamped api.ContainerMetricHistoryRes
	if err := json.Unmarshal(body, &clamped); err != nil {
		t.Fatal(err)
	}
	if clamped.To-clamped.From != 7*86400 {
		t.Fatalf("区间应夹到 7 天，实际 %d", clamped.To-clamped.From)
	}
	if clamped.BucketSeconds != 1680 {
		t.Fatalf("7 天区间应降采样到 1680 秒一桶，实际 %d", clamped.BucketSeconds)
	}
}

// 删除编排/服务器后，历史必须一起消失，不能留下查不到入口的孤儿行。
func TestMonitorHistoryPurgedWhenAgentDeleted(t *testing.T) {
	e := newTestEnv(t)
	admin := e.login(t)
	e.seedAgent(t, "purge-agent")
	seedHistory(t, e.db, "purge-agent", "abc123", "proj", "web")

	rec, body := e.do(t, http.MethodDelete, "/api/agents/purge-agent", "", admin)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, body)
	}
	points, err := e.db.QueryMonitorSamples(context.Background(), "purge-agent", "abc123", 0, time.Now().Unix()+60, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 0 {
		t.Fatalf("删除服务器后仍有 %d 条历史残留", len(points))
	}
}
