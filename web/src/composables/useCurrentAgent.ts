/**
 * 当前选中服务器的上下文
 *
 * 背景：改造后 URL 形如 `/agents/:id/containers`、`/agents/:id/stacks` 等，
 * 「当前是哪台服务器」这件事被布局（侧边菜单）、面包屑、以及每个操作页面共用。
 * 若各自读 `route.params.id` 再各算一遍，容易出现不一致（比如某页忘了取 id）。
 *
 * 因此统一在这里推导：
 * - `agentId`：URL 里的 `:id`，未选中服务器时为 null；
 * - `agent`：从 agents store 里取到的服务器对象，用于拿名字、在线状态；
 * - `isOnline`：决定操作类按钮是否可用（离线服务器上的操作必然失败，先禁用更友好）。
 *
 * ## 深链必须自己保证列表已加载
 *
 * 服务器列表原本只在列表页（AgentListView）里拉取。
 * 但用户完全可能直接打开 `/agents/xxx/stacks` 这样的深链，
 * 此时 store 是空的，`agent` 为 undefined，`isOnline` 会算成 false，
 * 页面就会错误地显示「服务器离线」并禁用所有按钮——
 * 而实际上服务器好好在线。这是实测发现并修掉的真 bug。
 *
 * 解决：`ensureAgentsLoaded()` 在列表从未加载成功过时补拉一次。
 * 用 store 里的 `loaded` 标志判断，避免每个页面都重复请求。
 */

import { computed, type ComputedRef } from 'vue'
import { useRoute } from 'vue-router'
import { useAgentsStore } from '@/stores/agents'
import type { Agent } from '@/types/agent'

export interface UseCurrentAgentReturn {
  /** 当前服务器 id；未选中时为 null */
  agentId: ComputedRef<string | null>
  /** 当前服务器对象（可能尚未加载到，此时为 undefined） */
  agent: ComputedRef<Agent | undefined>
  /** 页面标题用的展示名：优先服务器名，退化到 id，再退化到「服务器」 */
  displayName: ComputedRef<string>
  /** 当前服务器是否在线 */
  isOnline: ComputedRef<boolean>
  /** 确保服务器列表已加载（深链进入时补拉一次），返回 Promise 便于调用方 await */
  ensureAgentsLoaded: () => Promise<void>
}

/**
 * 读取当前服务器上下文。
 *
 * 边界：路由不是 `/agents/:id/*` 时（例如服务器列表页），`agentId` 为 null，
 * 调用方应据此隐藏「操作」类 UI，而不是拿空字符串去拼接口路径。
 */
export function useCurrentAgent(): UseCurrentAgentReturn {
  const route = useRoute()
  const agentsStore = useAgentsStore()

  const agentId = computed<string | null>(() => {
    const rawId = route.params.id
    // vue-router 的 params.id 可能是 string | string[]，数组时取第一个
    const value = Array.isArray(rawId) ? rawId[0] : rawId
    if (typeof value !== 'string' || value === '') return null
    return value
  })

  const agent = computed<Agent | undefined>(() =>
    agentId.value === null ? undefined : agentsStore.findById(agentId.value)
  )

  const displayName = computed<string>(() => agent.value?.name ?? agentId.value ?? '服务器')

  const isOnline = computed<boolean>(() => agent.value?.status === 'online')

  /**
   * 补拉服务器列表。
   *
   * 只在「从未成功加载过」时请求：`loaded` 由 store 在成功响应后置 true，
   * 因此重复进入不同操作页不会反复请求。
   * 失败不抛异常——页面会退化为「未找到该服务器」的提示，总好过整页报错。
   */
  async function ensureAgentsLoaded(): Promise<void> {
    if (agentsStore.loaded || agentsStore.loading) return
    await agentsStore.refresh()
  }

  return { agentId, agent, displayName, isOnline, ensureAgentsLoaded }
}
