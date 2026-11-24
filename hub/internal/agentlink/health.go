package agentlink

import (
	"sync/atomic"
	"time"

	"arkade/protocol/envelope"
)

// AgentHealth 只保存当前连接的最新样本，以 Hub 接收时间为准，不受远端时钟影响。
type AgentHealth struct {
	SampledAt   int64                 `json:"sampled_at"`
	RuntimeOK   *bool                 `json:"runtime_ok,omitempty"`
	HostMetrics *envelope.HostMetrics `json:"host_metrics,omitempty"`
}

type healthSnapshot struct {
	at   time.Time
	data AgentHealth
}

type healthSample struct {
	latest atomic.Pointer[healthSnapshot]
}

func (s *healthSample) record(runtimeOK *bool, metrics *envelope.HostMetrics, now time.Time) {
	metrics = validHostMetrics(metrics)
	if runtimeOK == nil && metrics == nil {
		s.latest.Store(nil)
		return
	}
	s.latest.Store(&healthSnapshot{at: now, data: AgentHealth{SampledAt: now.Unix(), RuntimeOK: runtimeOK, HostMetrics: metrics}})
}

// 丢弃无效指标，不影响同一心跳里的其他字段，也不将缺失数据解释为零用量。
func validHostMetrics(metrics *envelope.HostMetrics) *envelope.HostMetrics {
	if metrics == nil {
		return nil
	}
	var out envelope.HostMetrics
	if cpu := metrics.CPUPercent; cpu != nil && *cpu >= 0 && *cpu <= 100 {
		out.CPUPercent = cpu
	}
	if m := metrics.Memory; m != nil && m.TotalBytes > 0 && m.UsedBytes <= m.TotalBytes {
		out.Memory = m
	}
	if d := metrics.Disk; d != nil && d.TotalBytes > 0 && d.UsedBytes <= d.TotalBytes &&
		(d.Scope == envelope.DiskScopeHostRoot || d.Scope == envelope.DiskScopeHostFilesystem) {
		out.Disk = d
	}
	// 速率是从累计计数器求差得到的非负值，没有固定上限，无法按范围校验；
	// 只要字段存在就采信，字段缺失仍保持「没有采样」而不是 0。
	if n := metrics.Network; n != nil {
		out.Network = n
	}
	if io := metrics.DiskIO; io != nil {
		out.DiskIO = io
	}
	if out.CPUPercent == nil && out.Memory == nil && out.Disk == nil && out.Network == nil && out.DiskIO == nil {
		return nil
	}
	return &out
}

func (s *healthSample) current(now time.Time) *AgentHealth {
	if latest := s.latest.Load(); latest != nil && now.Sub(latest.at) < heartbeatTimeout {
		return &latest.data
	}
	return nil
}

// Health 返回在线连接的当前样本。重连/注销会自然丢弃上一条连接的样本。
// 仅在调用方已确认服务器查看权限后读取。
func (r *Registry) Health(agentID string) *AgentHealth {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if local := r.locals[agentID]; local != nil {
		return local.health.current(time.Now())
	}
	if conn := r.conns[agentID]; conn != nil {
		return conn.health.current(time.Now())
	}
	return nil
}

// LocalHealthRecorder 绑定当前注册的本机执行器。注销后，即使旧采样尚未完成，
// 迟到的样本也只会写入已不可见的旧实例。
func (r *Registry) LocalHealthRecorder(agentID string) func(bool, *envelope.HostMetrics) {
	r.mu.RLock()
	local := r.locals[agentID]
	r.mu.RUnlock()
	return func(runtimeOK bool, metrics *envelope.HostMetrics) {
		if local != nil {
			local.health.record(&runtimeOK, metrics, time.Now())
		}
	}
}
