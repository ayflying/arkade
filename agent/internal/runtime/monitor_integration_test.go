package runtime

import (
	"context"
	"encoding/hex"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMonitorDockerIntegration 仅在显式提供端点时采样现有容器。
// 测试只调用 GET，不创建、启停或删除容器，也不输出 inspect 原文。
func TestMonitorDockerIntegration(t *testing.T) {
	endpoint := os.Getenv("ARKADE_TEST_DOCKER_ENDPOINT")
	if endpoint == "" {
		t.Skip("设置 ARKADE_TEST_DOCKER_ENDPOINT 后运行只读 Docker 集成测试")
	}
	client, err := newClient(endpoint)
	if err != nil {
		t.Fatal("构造 Docker 客户端失败")
	}
	collector := (&engine{client: client}).NewMonitorCollector()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	first := collector.Snapshot(ctx)
	if !first.Complete {
		t.Fatalf("首次快照不完整: %s", first.Error)
	}
	if len(first.Containers) == 0 {
		t.Skip("运行时没有现有容器，不能验证真实指标；测试不会创建容器")
	}
	// one-shot 的首样本可能没有 preCPU，短暂间隔后验证缓存二采样。
	select {
	case <-time.After(300 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal("等待二采样时超时")
	}
	second := collector.Snapshot(ctx)
	if !second.Complete {
		t.Fatalf("第二次快照不完整: %s", second.Error)
	}
	previous := make(map[string]ContainerMetric, len(first.Containers))
	for _, metric := range first.Containers {
		previous[metric.ID] = metric
	}
	running, available := 0, 0
	for _, metric := range second.Containers {
		// Docker ID 必须完整，不能返回短 ID 或容器名冒充 ID。
		if len(metric.ID) != 64 {
			t.Fatalf("容器 ID 非完整 64 位 ID: 长度=%d", len(metric.ID))
		}
		if _, err := hex.DecodeString(metric.ID); err != nil {
			t.Fatal("容器 ID 不是合法十六进制")
		}
		if metric.State == "" || metric.Name == "" || metric.Image == "" {
			t.Fatal("容器状态/名称/镜像缺失")
		}
		if metric.State != "running" {
			if metric.StatsAvailable || !strings.Contains(metric.Error, "unavailable") {
				t.Fatal("非运行容器伪造可用统计")
			}
			continue
		}
		running++
		if !metric.StatsAvailable {
			// Docker/Podman 某些宿主机不提供 blkio/network，必须明确 unavailable，
			// 不能把缺失字段的默认零标成有效样本。
			if !strings.Contains(metric.Error, "unavailable") {
				t.Fatal("运行容器统计缺失但没有明确 unavailable 原因")
			}
			t.Logf("容器 %s 统计不可用（不伪造有效零）: %s", metric.ID[:12], metric.Error)
			continue
		}
		available++
		if metric.Error != "" || metric.MemoryBytes < 0 || metric.MemoryLimitBytes <= 0 || metric.MemoryPercent < 0 {
			t.Fatal("可用样本的内存指标或错误标记异常")
		}
		if metric.CPUPercent < 0 || math.IsNaN(metric.CPUPercent) || math.IsInf(metric.CPUPercent, 0) {
			t.Fatal("二采样 CPU 必须是非负有限百分比")
		}
		wantMemory := float64(metric.MemoryBytes) / float64(metric.MemoryLimitBytes) * 100
		if math.Abs(metric.MemoryPercent-wantMemory) > 0.000001 {
			t.Fatal("内存百分比不符合 usage/limit 公式")
		}
		if old, ok := previous[metric.ID]; ok && old.StatsAvailable && old.StartedAt == metric.StartedAt {
			if metric.NetworkRXBytes < old.NetworkRXBytes || metric.NetworkTXBytes < old.NetworkTXBytes || metric.BlockReadBytes < old.BlockReadBytes || metric.BlockWriteBytes < old.BlockWriteBytes {
				t.Fatal("未重启容器累计计数在二采样时回退")
			}
		}
		t.Logf("容器 %s CPU=%.3f%% memory=%d/%d", metric.ID[:12], metric.CPUPercent, metric.MemoryBytes, metric.MemoryLimitBytes)
	}
	if running == 0 {
		t.Skip("没有现有运行容器，无法验证 CPU/内存；测试不会启动容器")
	}
	t.Logf("完成只读二采样：容器=%d，运行=%d，完整资源统计=%d，不可用=%d", len(second.Containers), running, available, running-available)
}
