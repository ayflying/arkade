import type { ContainerMetricHistory, ContainerMetricsSnapshot } from '@/types/containerMetrics'
import { httpGet } from './request'

/** Read the Hub's cached snapshot once per server, not once per container. */
export function getContainerMetrics(agentId: string): Promise<ContainerMetricsSnapshot> {
  return httpGet<ContainerMetricsSnapshot>(`/agents/${encodeURIComponent(agentId)}/container-metrics`)
}

/**
 * 读取单个容器的资源历史。
 *
 * 服务端会把区间夹到 7 天保留期内，并按跨度自动降采样，所以这里不做任何
 * 区间校验或抽样——传原始意图，以响应里的 from/to/bucket_seconds 为准。
 */
export function getContainerMetricHistory(
  agentId: string,
  containerId: string,
  from: number,
  to: number
): Promise<ContainerMetricHistory> {
  const params = new URLSearchParams({ from: String(from), to: String(to) })
  return httpGet<ContainerMetricHistory>(
    `/agents/${encodeURIComponent(agentId)}/containers/${encodeURIComponent(containerId)}/metrics/history?${params.toString()}`
  )
}
