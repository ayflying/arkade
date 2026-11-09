/**
 * 镜像任务的状态与轮询（页面内任务列表用）
 *
 * ## 为什么状态放在这里而不是组件里
 *
 * 需求明确「切页后回到列表可恢复」：任务本身存在 **Hub 内存**中，
 * 前端不需要自己持久化，只要回到页面时**重新拉一次列表**即可恢复。
 *
 * 所以这个 composable 的职责是：
 * - 列表加载（进入页面/回到页面时能重新拉）；
 * - 有活跃任务时轮询刷新；
 * - 轮询间隔随状态变化（活跃任务多就勤一点）。
 *
 * ## 轮询为什么不能用全局「自动刷新」开关
 *
 * `usePolling` 会读 `uiSettings.autoRefresh`，关掉就完全停表。
 * 但用户关掉「列表自动刷新」表达的意图是「别一直刷列表」，不是
 * 「我提交的镜像导入不用告诉我进度」。所以这里直接对这个
 * `enabled` 传自己的条件，并**不**依赖全局开关——
 * 需要注意 `usePolling` 内部已强制叠加全局开关，因此本文件改用
 * 自己的定时器实现，避免任务进度被误停。
 *
 * 代价是要自己处理可见性与清理，下面都做了（与 usePolling 同等标准）：
 * 页面隐藏停表、切回立即刷一次、卸载清理、失败退避、防请求堆积。
 */

import { computed, onScopeDispose, ref } from 'vue'
import type { ComputedRef, Ref } from 'vue'
import { getImageTask, listImageTasks } from '@/api/imageTasks'
import type { ImageTask } from '@/types/imageTask'

/** 有任务在跑时的刷新间隔（毫秒） */
const ACTIVE_INTERVAL = 2000
/** 全部任务已结束时的刷新间隔：仍刷一次是为了感知新提交的任务 */
const IDLE_INTERVAL = 8000
/** 连续失败退避上限倍数 */
const MAX_BACKOFF_FACTOR = 8

/** 任务是否处于「已结束」状态 */
export function isTaskFinished(task: ImageTask): boolean {
  return task.status === 'succeeded' || task.status === 'failed' || task.status === 'canceled'
}

/** 任务是否处于「会继续变化」状态 */
export function isTaskActive(task: ImageTask): boolean {
  return task.status === 'pending' || task.status === 'running'
}

export interface UseImageTasksReturn {
  /** 当前服务器的任务列表（新的在前） */
  tasks: Ref<ImageTask[]>
  /** 是否正在加载 */
  loading: Ref<boolean>
  /** 加载过至少一次 */
  loaded: Ref<boolean>
  /** 列表级错误（不影响已拿到的任务） */
  errorText: Ref<string>
  /** 是否有活跃任务（决定轮询频率，也用于界面提示） */
  hasActive: ComputedRef<boolean>
  /** 立即拉一次 */
  refresh: () => Promise<void>
  /**
   * 把一个刚提交的任务插到列表顶部。
   *
   * 提交接口返回 202 + task，立刻插进去用户就能看见「进行中」，
   * 不必等下一轮轮询才出现。
   */
  upsert: (task: ImageTask) => void
}

function isVisible(): boolean {
  if (typeof document === 'undefined') return true
  return document.visibilityState !== 'hidden'
}

/**
 * 创建任务列表状态并启动轮询。
 *
 * @param getAgentId 取当前服务器 id；未选中时返回 null（此时不发请求）
 */
export function useImageTasks(getAgentId: () => string | null): UseImageTasksReturn {
  const tasks = ref<ImageTask[]>([])
  const loading = ref(false)
  const loaded = ref(false)
  const errorText = ref('')

  let timer: ReturnType<typeof setTimeout> | null = null
  let disposed = false
  let running = false
  let failureCount = 0

  const hasActive = computed(() => tasks.value.some(isTaskActive))

  function clearTimer(): void {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  /** 下一轮延迟：活跃时快、空闲时慢，连续失败翻倍退避 */
  function nextDelay(): number {
    const base = hasActive.value ? ACTIVE_INTERVAL : IDLE_INTERVAL
    const factor = Math.min(2 ** failureCount, MAX_BACKOFF_FACTOR)
    return base * factor
  }

  function schedule(): void {
    if (disposed) return
    clearTimer()
    timer = setTimeout(() => {
      void tick()
    }, nextDelay())
  }

  async function tick(): Promise<void> {
    if (disposed) return
    // 页面隐藏时不上网：停表，等 visibilitychange 再恢复
    if (!isVisible()) {
      schedule()
      return
    }
    await refresh()
    schedule()
  }

  async function refresh(): Promise<void> {
    const agentId = getAgentId()
    if (agentId === null) {
      tasks.value = []
      loaded.value = true
      return
    }
    // 防堆积：上一轮还没回来就不再发
    if (running) return
    running = true
    loading.value = true
    try {
      const list = await listImageTasks(agentId)
      // 新的在前：用户最关心刚提交的那个
      tasks.value = list.slice().sort((a, b) => b.created_at.localeCompare(a.created_at))
      loaded.value = true
      errorText.value = ''
      failureCount = 0
    } catch (err) {
      failureCount += 1
      /*
       * 保留已有 tasks：一次拉取失败不该把用户正在看的进度清空。
       * 只把错误放到 errorText 上，界面在列表上方提示「刷新失败，正在重试」。
       */
      errorText.value = err instanceof Error ? err.message : '加载镜像任务失败'
      loaded.value = true
    } finally {
      running = false
      loading.value = false
    }
  }

  /** 用单个任务的最新状态替换列表中的同 id 项；没有就插到顶部 */
  function upsert(task: ImageTask): void {
    const index = tasks.value.findIndex((t) => t.id === task.id)
    if (index >= 0) {
      tasks.value.splice(index, 1, task)
      return
    }
    tasks.value.unshift(task)
  }

  /** 页面从后台切回来：立即刷一次，别让用户盯着过期进度 */
  function handleVisibilityChange(): void {
    if (disposed) return
    if (isVisible()) {
      void tick()
    } else {
      clearTimer()
    }
  }

  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', handleVisibilityChange)
  }

  onScopeDispose(() => {
    disposed = true
    clearTimer()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  })

  // 启动：立刻拉一次再排程
  void tick()

  return { tasks, loading, loaded, errorText, hasActive, refresh, upsert }
}

/**
 * 轮询单个任务直到结束。
 *
 * 用于「刚提交一个任务，想立刻看到它完成」的场景（例如弹出层里的进度）。
 * 与列表轮询并存不会冲突：两者都只读，且都会在结束时停。
 *
 * @returns 结束时的任务；若页面卸载则返回 null
 */
export async function pollTaskUntilFinished(
  taskId: string,
  onUpdate: (task: ImageTask) => void,
  options: { interval?: number; timeoutMs?: number } = {}
): Promise<ImageTask | null> {
  const interval = options.interval ?? ACTIVE_INTERVAL
  const startedAt = Date.now()
  // 兜底上限：避免后端永远不结束时无限轮询（默认 60 分钟）
  const timeoutMs = options.timeoutMs ?? 60 * 60 * 1000

  for (;;) {
    const task = await getImageTask(taskId)
    onUpdate(task)
    if (isTaskFinished(task)) return task
    if (Date.now() - startedAt > timeoutMs) return task
    await new Promise<void>((resolve) => {
      setTimeout(resolve, interval)
    })
  }
}
