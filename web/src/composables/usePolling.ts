/**
 * 轮询组合式函数
 *
 * 前端所有「定时自动刷新」都应走这里，不要在各页面各写一套 setInterval。
 * 各写一套的结果是：清理逻辑遗漏、页面隐藏不停、后端挂了还狂发请求——
 * 这几个问题每一处都要单独修一遍，且很难发现。
 *
 * ## 统一处理了五件事
 *
 * 1. **启停**：由调用方给的「是否应该跑」条件控制（例如抽屉关闭、日志实时开关关闭）。
 * 2. **组件卸载清理**：onScopeDispose 自动停表，不会留下孤儿定时器。
 * 3. **页面不可见暂停**：`document.visibilityState === 'hidden'` 时停表。
 *    用户开着标签页去干别的很常见，不停就是纯浪费带宽。
 * 4. **切回来立即刷新一次**：而不是干等下一个周期，否则用户回来看到的是旧数据。
 * 5. **连续失败退避**：失败一次就把间隔翻倍（有上限），成功一次立刻恢复基础间隔。
 *    后端出问题时，按原频率狂发请求只会雪上加霜，且这些请求基本注定失败。
 *
 * ## 为什么不用 setInterval
 *
 * 用 `setTimeout` 自调度而不是 `setInterval`，因为：
 * - 每次回调执行时间不确定，固定间隔会让请求堆积（尤其叠加退避逻辑时）；
 * - 需要「每轮结束后再决定下次等多久」，`setInterval` 表达不了这件事。
 */

import { onScopeDispose, readonly, ref, watch } from 'vue'
import type { Ref } from 'vue'
import { useUiSettingsStore } from '@/stores/uiSettings'

/** 退避倍率上限：连续失败时最长等 baseInterval * MAX_BACKOFF_FACTOR */
const MAX_BACKOFF_FACTOR = 8

/**
 * 是否应该真正发请求，由「轮询启用」与「页面可见」共同决定。
 *
 * 注意这里读的是实时值而不是缓存：`visibilityState` 会在后台标签页里长期为 hidden，
 * 只要事件触发时再读一次即可，没必要自己做状态同步。
 */
function isVisible(): boolean {
  // SSR / 极端环境下没有 document，视为可见（不要把功能直接关死）
  if (typeof document === 'undefined') return true
  return document.visibilityState !== 'hidden'
}

export type PollingTask = () => void | Promise<void>

export interface UsePollingOptions {
  /** 任务名，仅用于调试时区分是哪个轮询在跑 */
  name?: string
  /**
   * 轮询间隔（毫秒）。
   *
   * 支持传函数：间隔需要随状态变化时用（例如日志区用户切换了拉取行数）。
   */
  interval: number | (() => number)
  /**
   * 是否启用轮询。支持传 ref 以便响应式启停。
   * 默认 true。
   */
  enabled?: Ref<boolean> | boolean
  /**
   * 是否在启用后立即执行一次。
   *
   * 默认 true——用户打开页面就该看到数据，而不是先空等一个周期。
   * 但如果调用方已经在 onMounted 里手动加载过，传 false 避免重复请求。
   */
  immediate?: boolean
  /**
   * 连续失败退避开关，默认 true。
   *
   * 会关掉它的场景极少。日志跟随是例外之一：
   * 那里失败要「保持原频率重试」，因为是用户在盯着看，退避会让重连变慢。
   */
  backoff?: boolean
}

export interface UsePollingReturn {
  /** 是否正在轮询（受 enabled 与页面可见性共同影响，便于界面展示「已暂停」） */
  active: Readonly<Ref<boolean>>
  /** 连续失败次数；调用方可用于展示「连接异常，正在重试」 */
  failureCount: Readonly<Ref<number>>
  /** 立即执行一次（不影响定时器节奏，也不重置已排好的下一次） */
  trigger: () => Promise<void>
  /** 手动暂停 / 恢复；与 enabled 条件叠加生效 */
  pause: () => void
  resume: () => void
}

/**
 * 创建一个轮询。
 *
 * 返回句柄供界面展示状态（是否在轮、连续失败几次），
 * 内部本身不处理错误——错误由 task 自己捕获并决定是否抛出：
 * - 抛出（或返回 rejected Promise）→ 计入失败次数并触发退避；
 * - 自己吞掉错误 → 视为成功，适合「失败也要按原频率重试」的场景。
 */
export function usePolling(task: PollingTask, options: UsePollingOptions): UsePollingReturn {
  const { interval, enabled = true, immediate = true, backoff = true, name = 'polling' } = options

  /**
   * 全局自动刷新开关。
   *
   * 在这里统一读取而不是让每个调用方自己传，是为了保证「关掉开关后无任何轮询」
   * 这条验收标准只依赖一处实现——只要用的是 usePolling，就一定受影响，
   * 不会出现某个页面忘了接开关而继续轮询。
   */
  const ui = useUiSettingsStore()

  /** 内部手动暂停标记（与 enabled 条件叠加） */
  const manuallyPaused = ref(false)
  /** 页面是否可见 */
  const visible = ref(isVisible())
  /** 是否正在轮询 */
  const active = ref(false)
  /** 连续失败次数 */
  const failureCount = ref(0)

  let timer: ReturnType<typeof setTimeout> | null = null
  /** 已被销毁（组件卸载）后不再排程，防止清理竞态 */
  let disposed = false
  /** 正在执行任务：避免上一轮没结束又叠一轮（后端慢时尤其重要） */
  let running = false

  const isEnabled = (): boolean => (typeof enabled === 'boolean' ? enabled : enabled.value)

  /** 当前基础间隔（毫秒），做了防御性下限，避免调用方传 0 造成疯狂请求 */
  function baseInterval(): number {
    const raw = typeof interval === 'function' ? interval() : interval
    if (!Number.isFinite(raw) || raw <= 0) return 1000
    return Math.max(200, raw)
  }

  /** 按连续失败次数计算本次应等待的时长 */
  function nextDelay(): number {
    if (!backoff || failureCount.value === 0) return baseInterval()
    const factor = Math.min(2 ** failureCount.value, MAX_BACKOFF_FACTOR)
    return baseInterval() * factor
  }

  /**
   * 记录一次失败。
   *
   * 只在控制台留痕，不弹提示：自动刷新失败不该打断用户当前的操作，
   * 界面上的「连接异常」提示由调用方根据 failureCount 自行渲染。
   */
  function logFailure(): void {
    console.warn(`[轮询:${name}] 第 ${failureCount.value} 次失败，下次间隔 ${nextDelay()}ms`)
  }

  function clearTimer(): void {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  /** 安排下一次执行 */
  function schedule(): void {
    clearTimer()
    if (disposed || !shouldRun()) {
      active.value = false
      return
    }
    active.value = true
    timer = setTimeout(() => {
      void tick()
    }, nextDelay())
  }

  /** 是否应该继续轮询 */
  function shouldRun(): boolean {
    // 全局开关优先级最高：关掉后任何页面都不再轮询
    if (!ui.autoRefresh) return false
    return isEnabled() && visible.value && !manuallyPaused.value
  }

  /** 执行一轮任务，结束后排下一轮 */
  async function tick(): Promise<void> {
    if (disposed) return
    // 条件在等待期间可能已经变化（用户关了开关 / 切走了标签页），此时不再发请求
    if (!shouldRun()) {
      clearTimer()
      active.value = false
      return
    }
    if (running) {
      // 上一轮还没回来：跳过本轮，直接排下一次，避免请求堆积
      schedule()
      return
    }

    running = true
    try {
      await task()
      failureCount.value = 0
    } catch {
      // 任务自己没处理错误：记一次失败，下次按退避拉长间隔
      failureCount.value += 1
      logFailure()
    } finally {
      running = false
    }

    schedule()
  }

  /**
   * 立即执行一次。
   *
   * 与 tick 的区别：不重排定时器，避免「手动刷新把轮询节奏打乱」。
   * 但会把失败计数清零——用户主动见证了一次刷新，可以重新开始退避周期。
   */
  async function trigger(): Promise<void> {
    if (disposed || running) return
    running = true
    try {
      await task()
      failureCount.value = 0
    } catch {
      failureCount.value += 1
    } finally {
      running = false
    }
  }

  function pause(): void {
    manuallyPaused.value = true
  }

  function resume(): void {
    manuallyPaused.value = false
  }

  /**
   * 可见性变化处理。
   *
   * 切回可见时「立即刷一次」而不是等排程：
   * 用户离开期间数据可能早就变了，等他干等 5 秒才更新会以为页面卡住。
   */
  function handleVisibilityChange(): void {
    const nowVisible = isVisible()
    if (nowVisible === visible.value) return
    visible.value = nowVisible

    if (!nowVisible) {
      // 隐藏：立刻清表，不留待发请求
      clearTimer()
      active.value = false
      return
    }

    // 恢复可见：清掉旧排程，马上跑一次，之后由 schedule 接管
    clearTimer()
    failureCount.value = 0
    if (shouldRun()) {
      active.value = true
      void tick()
    }
  }

  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', handleVisibilityChange)
  }

  /** 启停由 watch 驱动：条件一变立刻生效，不必等下一次心跳才发现 */
  watch(
    () => shouldRun(),
    (should) => {
      if (should) {
        clearTimer()
        if (immediate) {
          void tick()
        } else {
          schedule()
        }
      } else {
        clearTimer()
        active.value = false
      }
    },
    { immediate: true }
  )
  /**
   * 组件卸载清理。
   *
   * 用 onScopeDispose 而不是 onUnmounted：它在组件卸载与 effectScope 停止时都会触发，
   * 因此这个函数也能安全地用在普通组件之外的作用域里。
   */
  onScopeDispose(() => {
    disposed = true
    clearTimer()
    active.value = false
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
  })

  return {
    active: readonly(active),
    failureCount: readonly(failureCount),
    trigger,
    pause,
    resume
  }
}
