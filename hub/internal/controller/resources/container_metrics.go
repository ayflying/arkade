package resources

import (
	"context"
	"math"
	"time"

	api "arkade/hub/api/resources/v1"
	"arkade/hub/internal/controller"
	"arkade/hub/internal/store"
	"arkade/hub/internal/user"
	"github.com/gogf/gf/v2/net/ghttp"
)

func (c *Controller) ContainerMetrics(ctx context.Context, req *api.ContainerMetricsReq) (*api.ContainerMetricsRes, error) {
	if r := ghttp.RequestFromCtx(ctx); r != nil {
		r.Response.Header().Set("Cache-Control", "no-store")
	}
	if err := c.access.Require(ctx, req.AgentID, user.PermView); err != nil {
		return nil, err
	}
	out := &api.ContainerMetricsRes{AgentID: req.AgentID, Containers: []api.ContainerMetric{}}
	if c.deps.ContainerMetrics == nil {
		return out, nil
	}
	metrics, after := c.deps.ContainerMetrics(req.AgentID)
	out.StaleAfterSeconds = after
	now := time.Now().Unix()
	online := c.deps.AgentOnline != nil && c.deps.AgentOnline(req.AgentID)
	for _, m := range metrics {
		row := api.ContainerMetric{ContainerID: m.ContainerID, At: m.ReceivedAt, Stale: !online || m.Stale(now, after)}
		row.StatsAvailable = m.StatsAvailable && m.MemoryBytes >= 0 && m.MemoryLimitBytes > 0 && validPercent(m.CPUPercent) && validPercent(m.MemoryPercent)
		if row.StatsAvailable {
			row.CPUPercent, row.MemoryPercent = &m.CPUPercent, &m.MemoryPercent
			row.MemoryBytes, row.MemoryLimitBytes = &m.MemoryBytes, &m.MemoryLimitBytes
		}
		row.NetworkAvailable = m.NetworkAvailable != nil && *m.NetworkAvailable
		if row.NetworkAvailable {
			row.NetworkRXBytes, row.NetworkTXBytes = &m.NetworkRXBytes, &m.NetworkTXBytes
			if rx, tx, ok := m.NetworkRates(); ok {
				row.RatesAvailable = true
				row.NetworkRXPerSecond, row.NetworkTXPerSecond = &rx, &tx
			}
		}
		out.Containers = append(out.Containers, row)
	}
	return out, nil
}

// ContainerMetricHistory 返回单个容器的资源历史。
//
// 只看该服务器的查看权限，不需要管理员：历史曲线和实时指标是同一份数据的
// 不同时间视图，不应出现"能看实时值、看不了昨天"的割裂。
func (c *Controller) ContainerMetricHistory(ctx context.Context, req *api.ContainerMetricHistoryReq) (*api.ContainerMetricHistoryRes, error) {
	if r := ghttp.RequestFromCtx(ctx); r != nil {
		r.Response.Header().Set("Cache-Control", "no-store")
	}
	if err := c.access.Require(ctx, req.AgentID, user.PermView); err != nil {
		return nil, err
	}
	if req.ContainerID == "" {
		return nil, controller.Failure("bad_request", "容器 ID 不能为空")
	}
	from, to, ok := historyWindow(req.From, req.To, time.Now().Unix())
	if !ok {
		return nil, controller.Failure("bad_request", "查询时间范围无效：资源历史只保留最近 7 天")
	}
	if c.deps.DB == nil {
		return nil, controller.ErrInternal
	}
	bucket := historyBucket(to - from)
	points, err := c.deps.DB.QueryMonitorSamples(ctx, req.AgentID, req.ContainerID, from, to, bucket)
	if err != nil {
		return nil, controller.ErrInternal
	}
	if points == nil {
		points = []store.MonitorSamplePoint{}
	}
	return &api.ContainerMetricHistoryRes{
		AgentID:       req.AgentID,
		ContainerID:   req.ContainerID,
		From:          from,
		To:            to,
		BucketSeconds: bucket,
		RetentionDays: store.MonitorSampleRetentionDays,
		Points:        points,
	}, nil
}

// historyWindow 归一化查询窗口，返回实际生效的 [from, to]。
//
// 夹紧而不是报错：前端只提供保留期内的预设，越界通常来自旧书签或手工改
// 参数。把窗口夹到可用范围并回填实际值，比返回一个空图更不容易被误读成
// "这段时间没有数据"。
func historyWindow(from, to, now int64) (int64, int64, bool) {
	if from <= 0 || to <= 0 {
		return 0, 0, false
	}
	maxSpan := int64(store.MonitorSampleRetentionDays) * 86400
	earliest := now - maxSpan
	if to <= earliest {
		return 0, 0, false
	}
	if to > now {
		to = now
	}
	if from < earliest {
		from = earliest
	}
	if from >= to {
		return 0, 0, false
	}
	if to-from > maxSpan {
		from = to - maxSpan
	}
	return from, to, true
}

// historyBucket 选择聚合桶宽：把窗口切成不超过 MaxMonitorHistoryPoints 个点，
// 并对齐到整分钟，避免坐标轴出现 97 秒这种读数。
func historyBucket(span int64) int64 {
	bucket := span / store.MaxMonitorHistoryPoints
	if bucket < store.MonitorSampleBucketSeconds {
		bucket = store.MonitorSampleBucketSeconds
	}
	if remainder := bucket % store.MonitorSampleBucketSeconds; remainder != 0 {
		bucket += store.MonitorSampleBucketSeconds - remainder
	}
	return bucket
}

func validPercent(v float64) bool { return v >= 0 && !math.IsInf(v, 0) }
