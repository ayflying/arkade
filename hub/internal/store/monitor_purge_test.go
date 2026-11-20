package store

import (
	"context"
	"testing"
)

// seedIncident 写入一条告警事件及其一条投递记录，返回各自 ID。
// fingerprint 唯一，所以用 agent/container/kind 拼一个稳定值。
func seedIncident(t *testing.T, db *DB, agent, container, kind string) (string, string) {
	t.Helper()
	ctx := context.Background()
	id := agent + "-" + container + "-" + kind
	if err := db.UpsertMonitorIncident(ctx, MonitorIncident{
		ID: id, Fingerprint: id, AgentID: agent, ContainerID: container,
		Kind: kind, Status: "firing", FirstSeen: 1, LastSeen: 2, DetailJSON: "{}",
	}); err != nil {
		t.Fatal(err)
	}
	deliveryID := "d-" + id
	if err := db.AddMonitorDelivery(ctx, MonitorDelivery{
		ID: deliveryID, IncidentID: id, Channel: "dingtalk", Event: "firing", Status: "ok", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	return id, deliveryID
}

func incidentExists(t *testing.T, db *DB, id string) bool {
	t.Helper()
	rows, err := db.ListMonitorIncidents(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

func deliveryExists(t *testing.T, db *DB, id string) bool {
	t.Helper()
	rows, err := db.ListMonitorDeliveries(context.Background(), 500)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

func sampleCount(t *testing.T, db *DB, agent, container string) int {
	t.Helper()
	points, err := db.QueryMonitorSamples(context.Background(), agent, container, 0, 10_000, 60)
	if err != nil {
		t.Fatal(err)
	}
	return len(points)
}

// 删除对象后不留残留：样本、告警事件、投递记录三张表都要清，且互不误伤。
func TestPurgeMonitorDataScopes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.UpsertMonitorSamples(ctx, []MonitorSample{
		mkSample("a1", "c1", "proj1", "web-1", 600, 600, 1),
		mkSample("a1", "c2", "proj2", "db-1", 600, 600, 2),
		mkSample("a2", "c3", "proj1", "web-1", 600, 600, 3),
	}); err != nil {
		t.Fatal(err)
	}
	p1, p1d := seedIncident(t, db, "a1", "c1", "cpu")
	p2, p2d := seedIncident(t, db, "a1", "c2", "memory")
	p3, p3d := seedIncident(t, db, "a2", "c3", "cpu")
	// 服务器级告警（例如失联）没有容器 ID，编排删除不应该动它。
	agentWide, agentWideDelivery := seedIncident(t, db, "a1", "", "agent_offline")

	// 删编排：只清这个项目，靠样本表反查容器 ID 定位告警。
	if err := db.PurgeMonitorProjectData(ctx, "a1", "proj1"); err != nil {
		t.Fatal(err)
	}
	if n := sampleCount(t, db, "a1", "c1"); n != 0 {
		t.Fatalf("编排删除后应清空样本，剩 %d", n)
	}
	if incidentExists(t, db, p1) || deliveryExists(t, db, p1d) {
		t.Fatal("编排删除后告警事件与投递记录应一并清除")
	}
	if sampleCount(t, db, "a1", "c2") != 1 || !incidentExists(t, db, p2) {
		t.Fatal("别的编排不应受影响")
	}
	if sampleCount(t, db, "a2", "c3") != 1 || !incidentExists(t, db, p3) {
		t.Fatal("别的服务器不应受影响")
	}
	if !incidentExists(t, db, agentWide) || !deliveryExists(t, db, agentWideDelivery) {
		t.Fatal("服务器级告警不属于任何编排，不应被编排删除清掉")
	}

	// 删容器：短 ID 前缀与容器名都要能命中，因为删除接口回显的引用可能不是完整 ID。
	for _, ref := range []string{"c2", "db-1"} {
		if err := db.PurgeMonitorContainerData(ctx, "a1", ref); err != nil {
			t.Fatal(err)
		}
	}
	if n := sampleCount(t, db, "a1", "c2"); n != 0 {
		t.Fatalf("按 c2/db-1 删除后仍有样本残留: %d", n)
	}
	if incidentExists(t, db, p2) || deliveryExists(t, db, p2d) {
		t.Fatal("按短 ID 删除容器后告警事件应一并清除")
	}

	// 删服务器：整台清空。
	if err := db.PurgeMonitorAgentData(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if incidentExists(t, db, agentWide) || deliveryExists(t, db, agentWideDelivery) {
		t.Fatal("删除服务器后服务器级告警应清除")
	}
	if sampleCount(t, db, "a2", "c3") != 1 || !incidentExists(t, db, p3) || !deliveryExists(t, db, p3d) {
		t.Fatal("别的服务器不应受影响")
	}
}

// 空参数必须是 no-op，不能因为空字符串匹配到整表。
func TestPurgeMonitorDataRejectsEmptyScope(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.UpsertMonitorSamples(ctx, []MonitorSample{mkSample("a1", "c1", "proj", "web", 600, 600, 1)}); err != nil {
		t.Fatal(err)
	}
	incident, delivery := seedIncident(t, db, "a1", "c1", "cpu")
	for _, err := range []error{
		db.PurgeMonitorAgentData(ctx, ""),
		db.PurgeMonitorProjectData(ctx, "a1", ""),
		db.PurgeMonitorContainerData(ctx, "a1", "  "),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if sampleCount(t, db, "a1", "c1") != 1 {
		t.Fatal("空参数不应删除样本")
	}
	if !incidentExists(t, db, incident) || !deliveryExists(t, db, delivery) {
		t.Fatal("空参数不应删除告警记录")
	}
}
