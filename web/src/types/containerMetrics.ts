export interface ContainerMetricSample {
  container_id: string
  /** Unix seconds, measured at the Hub. */
  at: number
  stale: boolean
  stats_available: boolean
  cpu_percent?: number | null
  memory_bytes?: number | null
  memory_limit_bytes?: number | null
  memory_percent?: number | null
  network_available: boolean
  network_rx_bytes?: number | null
  network_tx_bytes?: number | null
  rates_available: boolean
  network_rx_per_second?: number | null
  network_tx_per_second?: number | null
}

export interface ContainerMetricsSnapshot {
  agent_id: string
  stale_after_seconds: number
  containers: ContainerMetricSample[]
}

export type ContainerMetricsStatus = 'waiting' | 'unsupported' | 'forbidden' | 'error'

/**
 * 历史曲线的一个降采样点。
 *
 * 指针字段缺失（undefined）= 该时间桶没有可用样本，必须断线；
 * 这和"值为 0"是两件事，图表不能把缺口补成 0。
 */
export interface ContainerMetricHistoryPoint {
  /** Unix 秒，桶起点。 */
  at: number
  /** 这个点聚合了多少个原始分钟样本。 */
  samples: number
  stats_available: boolean
  cpu_percent?: number | null
  memory_bytes?: number | null
  memory_limit_bytes?: number | null
  memory_percent?: number | null
  network_available: boolean
  rates_available: boolean
  network_rx_per_second?: number | null
  network_tx_per_second?: number | null
  block_read_per_second?: number | null
  block_write_per_second?: number | null
}

export interface ContainerMetricHistory {
  agent_id: string
  container_id: string
  /** 服务端最终生效的区间（可能被夹到保留期内）。 */
  from: number
  to: number
  /** 每个点聚合的时间宽度，由服务端按跨度决定。 */
  bucket_seconds: number
  retention_days: number
  points: ContainerMetricHistoryPoint[]
}

/** 时间范围预设：自定义之外都由服务端夹到 7 天保留期内。 */
export type MetricHistoryRange = 'today' | 'yesterday' | 'week' | 'custom'
