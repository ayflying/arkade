/**
 * Agent（服务器）列表 Store
 *
 * 职责：全局的服务器列表缓存 + 刷新动作。
 * 容器数据按页面局部管理（容器列表页自己持有），不放这里——
 * 容器状态变化频繁，且页面切换后无需保留，塞全局会让失效逻辑变复杂。
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { listAgents } from '@/api/agents'
import { ApiError } from '@/types/api'
import type { Agent } from '@/types/agent'

export const useAgentsStore = defineStore('agents', () => {
  const agents = ref<Agent[]>([])
  /** 列表请求进行中（表格 loading 用） */
  const loading = ref(false)
  /** 最近一次加载失败的文案；成功时清空 */
  const errorText = ref('')
  /** 是否至少成功加载过一次：用于区分「首次加载为空」和「确实没有服务器」 */
  const loaded = ref(false)

  /** 在线服务器数量：给页面顶部做摘要展示 */
  const onlineCount = computed(() => agents.value.filter((a) => a.status === 'online').length)

  /**
   * 从后端拉取最新列表。
   *
   * 返回是否成功；失败时保留旧数据（只置 errorText），
   * 这样手动刷新失败不会把已有表格清空成一次白屏。
   */
  async function refresh(): Promise<boolean> {
    loading.value = true
    errorText.value = ''
    try {
      agents.value = await listAgents()
      loaded.value = true
      return true
    } catch (err) {
      errorText.value = err instanceof ApiError ? err.message : '加载服务器列表失败'
      return false
    } finally {
      loading.value = false
    }
  }

  /** 按 id 取单个 Agent：容器列表页据此判断该服务器是否在线（决定按钮是否可用） */
  function findById(id: string): Agent | undefined {
    return agents.value.find((a) => a.id === id)
  }

  return { agents, loading, errorText, loaded, onlineCount, refresh, findById }
})
