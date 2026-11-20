package store

import (
	"context"
	"testing"
)

// mkSample 构造一条分钟样本，bucket 是桶起点。
func mkSample(agent, container, project, name string, bucket, last int64, cpu float64) MonitorSample {
	return MonitorSample{
		AgentID: agent, ContainerID: container, Project: project, ContainerName: name,
		BucketAt: bucket, LastAt: last, State: "running",
		StatsAvailable: true, CPUPercent: cpu, MemoryBytes: 1024, MemoryLimitBytes: 4096, MemoryPercent: 25,
		NetworkAvailable: true, NetworkRXRate: 10, NetworkTXRate: 20,
		BlockAvailable: true, BlockReadRate: 30, BlockWriteRate: 40,
		RatesAvailable: true, SnapshotComplete: true,
	}
}

func TestMonitorSamplesUpsertKeepsLastOfBucket(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	const agent, container = "a1", "c1"
	// 同一个桶写两次：第二次覆盖第一次，行数不增加。
	if err := db.UpsertMonitorSamples(ctx, []MonitorSample{
		mkSample(agent, container, "proj", "web", 600, 610, 10),
		mkSample(agent, container, "proj", "web", 600, 650, 42),
	}); err != nil {
		t.Fatal(err)
	}
	points, err := db.QueryMonitorSamples(ctx, agent, container, 600, 660, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 1 {
		t.Fatalf("同桶应只有一行，实际 %d 行", len(points))
	}
	p := points[0]
	if p.At != 600 || p.CPUPercent == nil || *p.CPUPercent != 42 {
		t.Fatalf("同桶应保留最后一次的值: %+v", p)
	}
	if p.Samples != 1 {
		t.Fatalf("覆盖写后样本数应为 1，实际 %d", p.Samples)
	}
}

func TestMonitorSamplesDownsamplesByBucket(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	const agent, container = "a1", "c1"
	var rows []MonitorSample
	for i := int64(0); i < 6; i++ {
		rows = append(rows, mkSample(agent, container, "proj", "web", 600+i*60, 600+i*60+1, float64(i*10)))
	}
	if err := db.UpsertMonitorSamples(ctx, rows); err != nil {
		t.Fatal(err)
	}
	// 桶宽 300 秒：600 与 900 是两个桶起点，第一桶含 600..840 共 5 个样本。
	points, err := db.QueryMonitorSamples(ctx, agent, container, 600, 900, 300)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 2 {
		t.Fatalf("应按 300 秒桶聚合成 2 点，实际 %d", len(points))
	}
	if points[0].At != 600 || points[1].At != 900 {
		t.Fatalf("桶起点不对: %d %d", points[0].At, points[1].At)
	}
	if points[0].Samples != 5 {
		t.Fatalf("第一个桶应含 5 个样本，实际 %d", points[0].Samples)
	}
	// 0、10、20、30、40 的平均是 20。
	if points[0].CPUPercent == nil || *points[0].CPUPercent != 20 {
		t.Fatalf("CPU 均值不对: %+v", points[0].CPUPercent)
	}
}

// 没有可用样本的桶必须返回 nil 而不是 0：前端要据此断开曲线，
// 画成 0 会被读成"这一分钟 CPU 是 0%"。
func TestMonitorSamplesUnavailableStatsAreNil(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	s := mkSample("a1", "c1", "proj", "web", 600, 600, 0)
	s.StatsAvailable = false
	s.RatesAvailable = false
	if err := db.UpsertMonitorSamples(ctx, []MonitorSample{s}); err != nil {
		t.Fatal(err)
	}
	points, err := db.QueryMonitorSamples(ctx, "a1", "c1", 600, 660, 60)
	if err != nil || len(points) != 1 {
		t.Fatalf("查询失败 %v %d", err, len(points))
	}
	if points[0].CPUPercent != nil || points[0].MemoryBytes != nil || points[0].NetworkRXPerSecond != nil {
		t.Fatalf("不可用指标必须为 nil: %+v", points[0])
	}
}

func TestMonitorSamplesPruneOnlyRemovesOldRows(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.UpsertMonitorSamples(ctx, []MonitorSample{
		mkSample("a1", "c1", "proj", "web", 100, 100, 1),
		mkSample("a1", "c1", "proj", "web", 1000, 1000, 2),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneMonitorSamples(ctx, 500); err != nil {
		t.Fatal(err)
	}
	points, err := db.QueryMonitorSamples(ctx, "a1", "c1", 0, 2000, 60)
	// 1000 / 60 = 16 → 桶起点 960，是这行样本真正落到的位置。
	if err != nil || len(points) != 1 || points[0].At != 960 {
		t.Fatalf("只应留下新数据: %+v %v", points, err)
	}
	// 非正时间戳不删任何东西，避免调用方传 0 时清空全表。
	if err := db.PruneMonitorSamples(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if points, err = db.QueryMonitorSamples(ctx, "a1", "c1", 0, 2000, 60); err != nil || len(points) != 1 {
		t.Fatalf("0 不应删除数据: %d %v", len(points), err)
	}
}
