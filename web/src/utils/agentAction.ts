/**
 * Agent / 容器可操作性判定
 *
 * 集中在一处的理由：离线禁用规则要在「服务器列表的操作入口」和「容器列表的
 * 启停按钮」两处生效，docs/02 §1 明确要求离线时不能发一个永远不返回的请求，
 * 因此这里给出统一的 reason 文案，两个页面复用同一个判断。
 */

import type { Agent } from '@/types/agent'
import type { Container } from '@/types/container'

/** Agent 是否处于在线状态（只有 online 才允许下发操作） */
export function isAgentOnline(agent: Pick<Agent, 'status'> | null | undefined): boolean {
  return agent?.status === 'online'
}

/**
 * 判断容器是否可被启停，不可用时给出禁用原因（用于 tooltip）。
 *
 * 返回 null 表示可以操作。规则：
 * 1. Agent 不在线 → 不可操作（核心规则，避免请求悬挂）；
 * 2. 运行中：可停止；已停止：可启动；其他中间态（paused/restarting）不提供启停，
 *    避免用户看到点了没反应。
 */
export function containerActionBlockReason(
  agent: Agent | null | undefined,
  container: Pick<Container, 'state'>
): string | null {
  if (!isAgentOnline(agent)) {
    return agent?.status === 'offline'
      ? '服务器已离线，无法对容器执行操作'
      : '服务器当前不可操作（仅在线状态可执行操作）'
  }
  if (container.state === 'running') return null
  if (container.state === 'exited' || container.state === 'stopped' || container.state === 'created') {
    return null
  }
  return `容器当前状态为 ${container.state}，暂不支持启停`
}

/** 是否可停止容器（仅运行中可停） */
export function canStopContainer(state: string): boolean {
  return state === 'running'
}

/** 是否可启动容器（非运行中且非过渡态可启动） */
export function canStartContainer(state: string): boolean {
  return state === 'exited' || state === 'stopped' || state === 'created'
}

/**
 * 是否可重启容器。
 *
 * 范围比启停宽：**只要状态是明确的（非空、非 unknown）就允许重启**。
 * 原因：重启最常见的用途正是修「容器卡住/状态诡异」，
 * 如果只在 running 时才给点，恰恰在最需要它的时候点不了。
 * 过渡态（restarting/paused）也不拦——用户再点一次重启是合理的重试。
 */
export function canRestartContainer(state: string): boolean {
  return state !== '' && state !== 'unknown' && state !== 'dead'
}

/**
 * 容器是否正在运行（决定删除时是否需要 force）。
 *
 * 只有 running/paused/restarting 这类「容器还占着运行时」的状态需要 force；
 * 已停止的容器直接删即可。
 */
export function needsForceDelete(state: string): boolean {
  return state === 'running' || state === 'paused' || state === 'restarting'
}
