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
