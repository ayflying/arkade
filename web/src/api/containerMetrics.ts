import type { ContainerMetricsSnapshot } from '@/types/containerMetrics'
import { httpGet } from './request'

/** Read the Hub's cached snapshot once per server, not once per container. */
export function getContainerMetrics(agentId: string): Promise<ContainerMetricsSnapshot> {
  return httpGet<ContainerMetricsSnapshot>(`/agents/${encodeURIComponent(agentId)}/container-metrics`)
}
