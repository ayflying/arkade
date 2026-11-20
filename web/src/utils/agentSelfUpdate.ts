import type { AgentSelfUpdateStatus } from '../api/agents'
import type { Agent } from '../types/agent'

/** 仅把连接中断或非 Arkade 网关错误视为未确认；有业务错误码的 503 并不是断线。 */
export function isSelfUpdateSubmissionUnconfirmed(status: number, code: string): boolean {
  return (status === 0 && code === 'network') ||
    ((status === 502 || status === 503 || status === 504) && code === `http_${status}`)
}

type UpdateResult =
  | { kind: 'updated'; agent: Agent }
  | { kind: 'failed'; message?: string }
  | { kind: 'unconfirmed'; helperSucceeded: boolean; lastObservedVersion?: string }
  | { kind: 'cancelled' }

interface WatchOptions {
  agentId: string
  beforeVersion: string
  requestId?: string
  signal: AbortSignal
  loadAgents: (signal: AbortSignal) => Promise<Agent[]>
  loadStatus: (requestId: string, signal: AbortSignal) => Promise<AgentSelfUpdateStatus>
  intervalMs?: number
  timeoutMs?: number
}

const POLL_INTERVAL_MS = 2_000
const OBSERVATION_TIMEOUT_MS = 120_000
const READ_TIMEOUT_MS = 8_000

/** 等待时也响应取消，切走页面或超时不再发下一次请求。 */
function pause(ms: number, signal: AbortSignal): Promise<void> {
  if (signal.aborted) return Promise.resolve()
  return new Promise((resolve) => {
    const finish = () => {
      clearTimeout(timer)
      signal.removeEventListener('abort', finish)
      resolve()
    }
    const timer = setTimeout(finish, ms)
    signal.addEventListener('abort', finish, { once: true })
  })
}

/**
 * 版本与执行结果分别取证：同版在线可能是旧容器还没停；助手退出成功
 * 也只证明 Compose 命令成功。只有同一台服务器带着不同版本在线才算升级。
 */
export async function watchAgentSelfUpdate(options: WatchOptions): Promise<UpdateResult> {
  const { agentId, beforeVersion, requestId, signal, loadAgents, loadStatus } = options
  const deadline = AbortSignal.timeout(options.timeoutMs ?? OBSERVATION_TIMEOUT_MS)
  const observation = AbortSignal.any([signal, deadline])
  let helperSucceeded = false
  let lastObservedVersion: string | undefined

  while (!observation.aborted) {
    await pause(options.intervalMs ?? POLL_INTERVAL_MS, observation)
    if (observation.aborted) break
    const readSignal = AbortSignal.any([observation, AbortSignal.timeout(READ_TIMEOUT_MS)])

    // 读失败（重启中的连接中断、旧 Hub 没有状态接口等）不是执行失败。
    // 共享本轮读取的超时，避免任一接口迟迟不响应阻塞整个观察流程。
    const [agents, task] = await Promise.allSettled([
      loadAgents(readSignal),
      requestId && !helperSucceeded ? loadStatus(requestId, readSignal) : Promise.resolve(undefined)
    ])
    if (observation.aborted) break
    if (agents.status === 'fulfilled') {
      const agent = agents.value.find((entry) => entry.id === agentId)
      if (agent?.status === 'online' && agent.version) {
        lastObservedVersion = agent.version
        if (beforeVersion && agent.version !== beforeVersion) return { kind: 'updated', agent }
      }
    }
    if (task.status === 'fulfilled') {
      if (task.value?.state === 'failed') return { kind: 'failed', message: task.value.message }
      if (task.value?.state === 'succeeded') helperSucceeded = true
    }
  }
  if (signal.aborted) return { kind: 'cancelled' }
  return { kind: 'unconfirmed', helperSucceeded, lastObservedVersion }
}
