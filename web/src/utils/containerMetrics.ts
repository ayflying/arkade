import type { ContainerMetricSample, ContainerMetricsStatus } from '@/types/containerMetrics'
import { formatBytes } from './formatSize'

export const containerMetricsStatusText: Record<ContainerMetricsStatus, string> = {
  waiting: '暂无样本',
  unsupported: '指标不受支持',
  forbidden: '无权限查看指标',
  error: '指标读取失败'
}

export function indexContainerMetrics(samples: ContainerMetricSample[]): ReadonlyMap<string, ContainerMetricSample> {
  return new Map(samples.map((sample) => [sample.container_id, sample]))
}

export function sampleExpiresAt(sample: ContainerMetricSample, staleAfterSeconds: number): number {
  return sample.at > 0 && staleAfterSeconds > 0
    ? (sample.at + staleAfterSeconds) * 1000
    : Number.POSITIVE_INFINITY
}

export function isContainerSampleStale(sample: ContainerMetricSample, expiresAt: number, now: number): boolean {
  return sample.stale === true || now >= expiresAt
}

function isMetric(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
}

function percent(value: number | null | undefined): string {
  if (!isMetric(value)) return '—'
  if (value > 0 && value < 0.01) return '<0.01%'
  return `${Number(value.toFixed(2))}%`
}

function bytesPerSecond(value: number | null | undefined): string {
  if (!isMetric(value)) return '—'
  // A fractional rate is possible even though the underlying byte counters are integers.
  if (value > 0 && value < 1) return '<1 B/s'
  return `${formatBytes(value)}/s`
}

export function formatContainerMetrics(sample: ContainerMetricSample) {
  const stats = sample.stats_available === true
  const network = sample.network_available === true
  const rates = network && sample.rates_available === true
  return {
    cpu: stats ? percent(sample.cpu_percent) : '—',
    memory: stats ? formatBytes(sample.memory_bytes) : '—',
    rxRate: rates ? bytesPerSecond(sample.network_rx_per_second) : '—',
    txRate: rates ? bytesPerSecond(sample.network_tx_per_second) : '—',
    rxTotal: network ? formatBytes(sample.network_rx_bytes) : '—',
    txTotal: network ? formatBytes(sample.network_tx_bytes) : '—',
    resourceNote: stats ? '' : 'CPU / 内存暂不可用',
    networkNote: !network ? '网络统计不可用' : !rates ? '速率暂不可用' : ''
  }
}
