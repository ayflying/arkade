import type { Agent, HostByteUsage } from '../types/agent'

// Same validity window as the Hub. The UI also expires already-rendered samples
// when automatic refresh is off or the list cannot be refreshed.
export const SAMPLE_TTL_MS = 60_000

export const HEALTH_STATES = [
  { key: 'offline', label: '离线', tone: 'error' },
  { key: 'engine-error', label: '在线 · 引擎异常', tone: 'error' },
  { key: 'no-data', label: '在线 · 暂无采样', tone: 'warning' },
  { key: 'engine-unknown', label: '在线 · 引擎状态未知', tone: 'warning' },
  { key: 'upgrading', label: '升级中', tone: 'info' },
  { key: 'pending', label: '待接入', tone: 'default' },
  { key: 'disabled', label: '已禁用', tone: 'default' },
  { key: 'unknown', label: '状态未知', tone: 'warning' },
  { key: 'ready', label: '在线 · 引擎可达', tone: 'success' }
] as const

export type HealthState = typeof HEALTH_STATES[number]['key']

const ranks = new Map<HealthState, number>(HEALTH_STATES.map(({ key }, rank) => [key, rank]))

function validBytes(usage: HostByteUsage | undefined): HostByteUsage | undefined {
  if (!usage || !Number.isFinite(usage.used_bytes) || !Number.isFinite(usage.total_bytes)) return
  if (usage.total_bytes <= 0 || usage.used_bytes < 0 || usage.used_bytes > usage.total_bytes) return
  return usage
}

export function sampleExpiresAt(sampledAt: number | undefined, receivedAt: number): number {
  if (typeof sampledAt !== 'number' || !Number.isFinite(sampledAt) || sampledAt <= 0) return 0
  // Allow for a Hub clock ahead of the browser without keeping the sample forever.
  return Math.min(sampledAt * 1000, receivedAt) + SAMPLE_TTL_MS
}

function getHostSample(metrics: Agent['host_metrics']) {
  if (!metrics) return null

  const cpu = metrics.cpu_percent
  const cpuPercent = typeof cpu === 'number' && Number.isFinite(cpu) && cpu >= 0 && cpu <= 100 ? cpu : undefined
  const memory = validBytes(metrics.memory)
  const disk = (metrics.disk?.scope === 'host_root' || metrics.disk?.scope === 'host_filesystem') &&
    validBytes(metrics.disk) ? metrics.disk : undefined
  if (cpuPercent === undefined && !memory && !disk) return null
  return { cpuPercent, memory, disk }
}

export function getAgentHealth(agent: Agent, now: number, receivedAt: number) {
  const fresh = agent.status === 'online' && agent.disabled !== true &&
    sampleExpiresAt(agent.sampled_at, receivedAt) > now
  const sample = fresh ? getHostSample(agent.host_metrics) : null
  const runtimeOk = fresh ? agent.runtime_ok : undefined
  let state: HealthState
  if (agent.disabled === true) state = 'disabled'
  else if (agent.status === 'offline') state = 'offline'
  else if (agent.status === 'pending') state = 'pending'
  else if (agent.status === 'upgrading') state = 'upgrading'
  else if (agent.status !== 'online') state = 'unknown'
  else if (runtimeOk === false) state = 'engine-error'
  else if (!sample) state = 'no-data'
  else if (runtimeOk !== true) state = 'engine-unknown'
  else state = 'ready'

  const rank = ranks.get(state)!
  return { agent, sample, runtimeOk, rank, state: HEALTH_STATES[rank]! }
}
