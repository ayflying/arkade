package resources

import (
	"context"
	"math"
	"time"

	api "arkade/hub/api/resources/v1"
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

func validPercent(v float64) bool { return v >= 0 && !math.IsInf(v, 0) }
