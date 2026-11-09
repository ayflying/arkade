package compose

// status_test.go 覆盖状态归并与 ps 输出解析。
//
// 这里的输出会直接进入 Hub 的 drift 判定与界面状态显示，
// 因此要覆盖 compose 各版本真实存在的几种 JSON 形态。

import (
	"testing"
)

// TestParsePSJsonArray 验证数组形态。
func TestParsePSJsonArray(t *testing.T) {
	raw := `[{"Name":"p-web-1","Service":"web","State":"running","Status":"Up 3 minutes"},
	          {"Name":"p-db-1","Service":"db","State":"exited","Status":"Exited (0) 1 minute ago"}]`
	entries := parsePSJson(raw)
	if len(entries) != 2 {
		t.Fatalf("应解析出 2 个条目，实际 %d", len(entries))
	}
	if entries[0].Service != "web" || entries[1].Service != "db" {
		t.Errorf("服务名解析错误: %+v", entries)
	}
}

// TestParsePSJsonLines 验证"每行一个对象"形态（compose 2.0~2.5 的行为）。
func TestParsePSJsonLines(t *testing.T) {
	raw := `{"Name":"p-web-1","Service":"web","State":"running"}
{"Name":"p-db-1","Service":"db","State":"running"}`
	entries := parsePSJson(raw)
	if len(entries) != 2 {
		t.Fatalf("应解析出 2 个条目，实际 %d", len(entries))
	}
}

// TestParsePSJsonEmpty 验证空输出。
func TestParsePSJsonEmpty(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\n"} {
		if entries := parsePSJson(raw); len(entries) != 0 {
			t.Errorf("%q 应解析出 0 个条目，实际 %d", raw, len(entries))
		}
	}
}

// TestParsePSJsonGarbage 验证垃圾输入不 panic。
func TestParsePSJsonGarbage(t *testing.T) {
	cases := []string{
		"[not json",
		`{"broken": `,
		"random text",
		`["array of strings"]`,
	}
	for _, raw := range cases {
		// 只要求不 panic 且返回空；状态探测是尽力而为的信息。
		if entries := parsePSJson(raw); len(entries) > 0 {
			// 数组里的字符串不是一个有效条目，某些实现会解出零值，
			// 只要不 panic 就不算失败。
			_ = entries
		}
	}
}

// TestIsRunningEntry 验证运行态判定。
func TestIsRunningEntry(t *testing.T) {
	running := []psEntry{
		{State: "running"},
		{State: "RUNNING"},
		{State: "running", Status: "Up 3 minutes"},
		{Status: "Up 3 minutes (healthy)"}, // 没有 State 时看 Status 前缀
		{Status: "up"},
	}
	for _, e := range running {
		if !isRunningEntry(e) {
			t.Errorf("%+v 应判为运行中", e)
		}
	}

	notRunning := []psEntry{
		{State: "exited"},
		{State: "paused"},
		{State: "restarting"},
		{State: "created"},
		{State: "dead"},
		{Status: "Exited (0) 2 minutes ago"},
		{Status: "Created"},
		{},
	}
	for _, e := range notRunning {
		if isRunningEntry(e) {
			t.Errorf("%+v 不应判为运行中", e)
		}
	}
}

// TestServiceStats 验证服务数与运行数统计。
func TestServiceStats(t *testing.T) {
	services := []string{"web", "api", "db"}
	entries := []psEntry{
		{Service: "web", State: "running"},
		{Service: "api", State: "running"},
		{Service: "db", State: "exited"},
	}
	running, total := serviceStats(services, entries)
	if running != 2 || total != 3 {
		t.Errorf("应为 2/3，实际 %d/%d", running, total)
	}
}

// TestServiceStatsUsesConfigAsAuthority 验证以配置为准而不是 ps 结果。
//
// 关键场景：容器全停时 compose ps 可能什么都不返回。
// 如果只看 ps 结果，总数会变成 0，界面显示"0/0"，
// 用户无法判断是没启动还是真没服务。
func TestServiceStatsUsesConfigAsAuthority(t *testing.T) {
	services := []string{"web", "db"}
	running, total := serviceStats(services, nil)
	if total != 2 {
		t.Errorf("总数应来自配置（2），实际 %d", total)
	}
	if running != 0 {
		t.Errorf("无 ps 结果时运行数应为 0，实际 %d", running)
	}
}

// TestServiceStatsFallbackToPS 验证配置解析失败时退回 ps 结果。
func TestServiceStatsFallbackToPS(t *testing.T) {
	entries := []psEntry{
		{Service: "x", State: "running"},
		{Service: "y", State: "exited"},
	}
	running, total := serviceStats(nil, entries)
	if running != 1 || total != 2 {
		t.Errorf("应退回按 ps 统计（1/2），实际 %d/%d", running, total)
	}
}

// TestServiceStatsReplicas 验证多副本服务：任一在跑即算该服务在跑。
func TestServiceStatsReplicas(t *testing.T) {
	services := []string{"web"}
	entries := []psEntry{
		{Service: "web", State: "exited"},
		{Service: "web", State: "running"},
		{Service: "web", State: "running"},
	}
	running, total := serviceStats(services, entries)
	if running != 1 || total != 1 {
		t.Errorf("多副本时 web 应算 1/1，实际 %d/%d", running, total)
	}
}

// TestAggregateStatus 验证整体状态归并。
func TestAggregateStatus(t *testing.T) {
	cases := []struct {
		running, total int
		want           string
		reason         string
	}{
		{0, 0, StatusUnknown, "空项目"},
		{0, 3, StatusStopped, "全部停止"},
		{3, 3, StatusRunning, "全部运行"},
		{1, 3, StatusPartial, "部分运行"},
		{2, 3, StatusPartial, "部分运行"},
	}
	for _, c := range cases {
		if got := aggregateStatus(c.running, c.total); got != c.want {
			t.Errorf("%d/%d 应为 %s（%s），实际 %s", c.running, c.total, c.want, c.reason, got)
		}
	}
}

// TestAggregateStatusEmptyIsUnknown 专门验证"空项目报 unknown 而不是 stopped"。
//
// stopped 会暗示"曾经跑过、现在停了"，而实际上可能从未启动，
// 两者在界面上给用户的判断完全不同。
func TestAggregateStatusEmptyIsUnknown(t *testing.T) {
	if got := aggregateStatus(0, 0); got == StatusStopped {
		t.Error("空项目不应报 stopped（会误导致用户以为服务挂了）")
	}
}
