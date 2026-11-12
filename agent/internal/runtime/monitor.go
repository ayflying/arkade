package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ContainerMonitorRuntime 是可选能力，不扩大 Runtime，兼容既有 fake。
type ContainerMonitorRuntime interface{ NewMonitorCollector() *MonitorCollector }

// MonitorSnapshot 是监控白名单视图。Error 只使用固定描述，绝不透传 daemon 原文。
type MonitorSnapshot struct {
	At         int64
	Containers []ContainerMetric
	Complete   bool
	Error      string
}
type ContainerMetric struct {
	inspectAvailable                                                bool
	ID, Name, Image, State, Health                                  string
	ExitCode, RestartCount                                          int
	StartedAt, FinishedAt                                           int64
	CPUPercent                                                      float64
	MemoryBytes, MemoryLimitBytes                                   int64
	MemoryPercent                                                   float64
	NetworkRXBytes, NetworkTXBytes, BlockReadBytes, BlockWriteBytes uint64
	StatsAvailable                                                  bool
	Error                                                           string
}

const monitorMaxContainers = 2000
const monitorTimeout = 15 * time.Second
const monitorBodyLimit = 16 << 20

// MonitorCollector 的缓存仅属于当前处理器，不使用全局表，不持有 inspect 内容。
type MonitorCollector struct {
	engine   *engine
	slots    chan struct{}
	mu       sync.Mutex
	previous map[string]monitorCPU
}

func (e *engine) NewMonitorCollector() *MonitorCollector {
	return &MonitorCollector{engine: e, previous: make(map[string]monitorCPU), slots: make(chan struct{}, 4)}
}

type monitorCPU struct {
	Usage struct {
		Total  uint64   `json:"total_usage"`
		PerCPU []uint64 `json:"percpu_usage"`
	} `json:"cpu_usage"`
	System uint64 `json:"system_cpu_usage"`
	Online uint64 `json:"online_cpus"`
}
type monitorStats struct {
	CPU    monitorCPU `json:"cpu_stats"`
	PreCPU monitorCPU `json:"precpu_stats"`
	Memory *struct {
		Usage *int64           `json:"usage"`
		Limit *int64           `json:"limit"`
		Stats map[string]int64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RX uint64 `json:"rx_bytes"`
		TX uint64 `json:"tx_bytes"`
	} `json:"networks"`
	Block *struct {
		Entries []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}
type monitorInspect struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	RestartCount int    `json:"RestartCount"`
	Config       struct {
		Image string `json:"Image"`
	} `json:"Config"`
	State struct {
		Status     string `json:"Status"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
}

// monitorGet 独立限制响应体并拒绝截断/尾随 JSON，所有错误均脱敏。
func (c *client) monitorGet(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(path), nil)
	if err != nil {
		return errors.New("monitor unavailable: invalid request")
	}
	resp, err := c.httpc.Do(req)
	if err != nil {
		return errors.New("monitor unavailable: request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("monitor unavailable: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, monitorBodyLimit+1))
	if err != nil {
		return errors.New("monitor unavailable: response read failed")
	}
	if len(body) > monitorBodyLimit {
		return errors.New("monitor unavailable: response too large")
	}
	if json.Unmarshal(body, out) != nil {
		return errors.New("monitor unavailable: invalid response")
	}
	return nil
}

// Snapshot 使用固定四个 worker；包括排队时间在内由请求上下文控制。
func (m *MonitorCollector) Snapshot(ctx context.Context) MonitorSnapshot {
	ctx, cancel := context.WithTimeout(ctx, monitorTimeout)
	defer cancel()
	out := MonitorSnapshot{At: time.Now().Unix(), Containers: []ContainerMetric{}, Complete: true}
	// 列表只声明必要字段，避免存储 Labels、Env 等无关信息。
	var list []struct {
		ID    string   `json:"Id"`
		Names []string `json:"Names"`
		Image string   `json:"Image"`
		State string   `json:"State"`
	}
	if err := m.engine.client.monitorGet(ctx, pathContainers+"?all=1", &list); err != nil {
		out.Complete = false
		out.Error = err.Error()
		return out
	}
	if len(list) > monitorMaxContainers {
		list = list[:monitorMaxContainers]
		out.Complete = false
		out.Error = "monitor incomplete: container limit 2000"
	}
	out.Containers = make([]ContainerMetric, len(list))
	for i, c := range list {
		out.Containers[i] = ContainerMetric{ID: c.ID, Name: firstContainerName(c.Names), Image: c.Image, State: c.State, Error: "stats unavailable: not collected"}
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				out.Containers[i] = m.collect(ctx, out.Containers[i])
			}
		}()
	}
dispatch:
	for i := range list {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	if ctx.Err() != nil {
		out.Complete = false
		out.Error = "monitor incomplete: request canceled or timed out"
	}
	for _, c := range out.Containers {
		if !c.inspectAvailable {
			out.Complete = false
			if out.Error == "" {
				out.Error = "monitor incomplete: some container inspections unavailable"
			}
		}
	}
	// 每轮清理消失容器，缓存规模与采集上限一致。
	alive := make(map[string]bool, len(list))
	for _, c := range list {
		alive[c.ID] = true
	}
	m.mu.Lock()
	for id := range m.previous {
		if !alive[id] {
			delete(m.previous, id)
		}
	}
	m.mu.Unlock()
	return out
}

func monitorUnix(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil || t.IsZero() || t.Unix() < 0 {
		return 0
	}
	return t.Unix()
}
func (m *MonitorCollector) collect(ctx context.Context, c ContainerMetric) ContainerMetric {
	// 信号量跨快照共享，重叠 RPC 也不会把 daemon 并发放大到四以上。
	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		c.Error = "stats unavailable: request canceled"
		return c
	}
	c.Error = ""
	if validateRef(c.ID) != nil {
		c.Error = "monitor unavailable: invalid container ID"
		return c
	}
	base := pathContainerPrefix + url.PathEscape(c.ID)
	var d monitorInspect
	if err := m.engine.client.monitorGet(ctx, base+"/json", &d); err != nil {
		c.Error = err.Error()
		return c
	}
	if d.State.Status == "" {
		c.Error = "monitor unavailable: inspect state missing"
		return c
	}
	c.inspectAvailable = true
	if d.ID != "" {
		c.ID = d.ID
	}
	if d.Name != "" {
		c.Name = strings.TrimPrefix(d.Name, "/")
	}
	if d.Config.Image != "" {
		c.Image = d.Config.Image
	}
	c.State = d.State.Status
	c.ExitCode = d.State.ExitCode
	c.RestartCount = d.RestartCount
	c.StartedAt = monitorUnix(d.State.StartedAt)
	c.FinishedAt = monitorUnix(d.State.FinishedAt)
	if d.State.Health != nil {
		c.Health = d.State.Health.Status
	}
	if c.State != "running" {
		c.Error = "stats unavailable: container not running"
		return c
	}
	var s monitorStats
	if err := m.engine.client.monitorGet(ctx, base+"/stats?stream=false&one-shot=true", &s); err != nil {
		c.Error = err.Error()
		return c
	}
	prev := s.PreCPU
	m.mu.Lock()
	if prev.System == 0 {
		prev = m.previous[c.ID]
	}
	// 系统计数回退可能是宿主机重启；保存新基线，下一轮可恢复，而非永久卡住。
	m.previous[c.ID] = s.CPU
	m.mu.Unlock()
	cpus := s.CPU.Online
	if cpus == 0 {
		cpus = uint64(len(s.CPU.Usage.PerCPU))
	}
	// CPU 标准公式：(容器增量 / 系统增量) * 在线核数 * 100。
	if prev.System == 0 || s.CPU.System <= prev.System || s.CPU.Usage.Total < prev.Usage.Total || cpus == 0 {
		c.Error = "stats unavailable: CPU baseline missing or reset"
		return c
	}
	if s.Memory == nil || s.Memory.Usage == nil || s.Memory.Limit == nil || *s.Memory.Limit <= 0 || *s.Memory.Usage < 0 {
		c.Error = "stats unavailable: memory accounting missing"
		return c
	}
	c.CPUPercent = float64(s.CPU.Usage.Total-prev.Usage.Total) / float64(s.CPU.System-prev.System) * float64(cpus) * 100
	if math.IsInf(c.CPUPercent, 0) || math.IsNaN(c.CPUPercent) {
		c.Error = "stats unavailable: invalid CPU delta"
		return c
	}
	// 与 docker stats 一致，优先扣除 cgroup v1/v2 的非活跃文件缓存。
	usage := *s.Memory.Usage
	cache, ok := s.Memory.Stats["total_inactive_file"]
	if !ok {
		cache = s.Memory.Stats["inactive_file"]
	}
	if cache > 0 && cache < usage {
		usage -= cache
	}
	c.MemoryBytes = usage
	c.MemoryLimitBytes = *s.Memory.Limit
	c.MemoryPercent = float64(usage) / float64(c.MemoryLimitBytes) * 100
	// 兼容引擎可能完全省略网络/块设备统计。缺失与真实零不可混淆。
	if s.Networks == nil || s.Block == nil || s.Block.Entries == nil {
		c.Error = "stats unavailable: network or block accounting missing"
		return c
	}
	for _, n := range s.Networks {
		c.NetworkRXBytes += n.RX
		c.NetworkTXBytes += n.TX
	}
	for _, b := range s.Block.Entries {
		switch strings.ToLower(b.Op) {
		case "read":
			c.BlockReadBytes += b.Value
		case "write":
			c.BlockWriteBytes += b.Value
		}
	}
	c.StatsAvailable = true
	return c
}
