/**
 * 编排「更新镜像」任务的状态机
 *
 * 只负责状态与时序；纯计算（合并/去重/裁剪/脱敏）在 `utils/stackPullOutput.ts`。
 *
 * 不变量：
 * 1. 一次点击一个任务，`request_id` 首次生成后长期复用；
 * 2. 网络不确定时绝不换 `request_id`（换了就绕过服务端幂等）；
 * 3. 读不到 ≠ 任务失败，只标记 interrupted 并保留输出；
 * 4. 响应必须属于当前任务（防串项目串状态）；
 * 5. 游标只前进；403/404 停止自动轮询，改手动重试；
 * 6. 关窗只 `pause()` 不取消任务，重开 `resume()` 接着读。
 */
import { computed, getCurrentInstance, onUnmounted, ref, shallowRef } from 'vue'
import type { StackPullTask } from '@/types/stackPullTask'
import { isConfirmedSuccess, isTerminal } from '@/types/stackPullTask'
import {
  MAX_OUTPUT_BYTES,
  MAX_OUTPUT_EVENTS,
  mergeOutput,
  stripAnsi,
  type OutputChunk
} from '@/utils/stackPullOutput'

/** 基础轮询间隔：1 秒 */
export const POLL_INTERVAL_MS = 1000
/** 网络类错误的最大退避间隔 */
export const MAX_BACKOFF_MS = 8000

/** 任务在界面上的可见阶段 */
export type PullPhase =
  | 'idle'
  | 'starting'
  | 'queued'
  | 'running'
  | 'done'
  | 'failed'
  | 'interrupted'
  | 'blocked'

export interface StackPullRunnerOptions {
  start: (requestId: string) => Promise<StackPullTask>
  fetch: (taskId: string, after: number) => Promise<StackPullTask>
  /** 期望的编排名，用于校验响应身份（缺省则不校验 name） */
  expectedName?: string
}

export function useStackPullTask(options: StackPullRunnerOptions) {
  const task = shallowRef<StackPullTask | null>(null)
  const output = shallowRef<OutputChunk[]>([])
  /** 有内容被省略（字节超限 / 条数超限 / 后端 truncated / seq 有缺口） */
  const dropped = ref(false)
  /** 具体因为什么省略，给界面分档提示 */
  const droppedReasons = ref<string[]>([])
  const interrupted = ref(false)
  const starting = ref(false)
  const errorText = ref('')
  const uncertain = ref(false)
  /** 永久性错误（403/404）：不再自动轮询，等用户手动重试 */
  const permanentError = ref(false)

  /**
   * 幂等键：第一次点击生成，永久复用（直到 reset 确认后清空）。
   * 跨重试、跨关闭重开都存活，这正是幂等的意义。
   */
  let requestId = ''
  let timer: ReturnType<typeof setTimeout> | null = null
  /** 轮询代数：每次 start/resume/stop 递增，丢弃过期响应 */
  let generation = 0
  let disposed = false
  /** 连续网络错误次数，用于退避 */
  let backoffAttempt = 0
  /** 已见过的最大 seq，用于检测缺口 */
  let maxSeenSeq = 0
  /** 已消费的最大游标，用于保证单调 */
  let maxCursor = 0
  /**
   * 启动请求是否在途。
   *
   * 关闭弹窗时 stopPoll 会 ++generation，如果 POST 还在途，
   * 它的结果会被 generation 守卫丢掉——但**服务器上任务已经建好了**。
   * 用这个标记记住「有个任务可能已创建但本地没拿到」，
   * 重开时按 uncertain 处理（同 request_id 重试），而不是当成全新任务。
   */
  let startInFlight = false
  /** 已发起过启动请求（哪怕结果丢了），重开时不能当 idle */
  let startAttempted = false

  const phase = computed<PullPhase>(() => {
    if (starting.value) return 'starting'
    const t = task.value
    if (t === null) {
      /*
       * 发起过启动请求却没拿到任务：状态未知，不能显示 idle。
       * 典型场景是 POST 在途时关窗，generation 失效导致结果被丢弃——
       * 服务器上可能已经有任务在跑，显示「尚未开始」会误导用户再点一次。
       * 此时 request_id 仍然保留，重试不会重复启动。
       */
      if (startAttempted) return 'blocked'
      return 'idle'
    }
    if (interrupted.value) return 'interrupted'
    if (t.status === 'queued') return 'queued'
    if (t.status === 'failed') return 'failed'
    if (t.status === 'succeeded') return isConfirmedSuccess(t) ? 'done' : 'failed'
    if (t.status === 'unknown') return 'blocked'
    return 'running'
  })

  const hasTask = computed(() => task.value !== null)

  const finished = computed(() => {
    const t = task.value
    return t !== null && isTerminal(t.status)
  })

  /**
   * 拼接后的纯文本（已脱敏），供界面插值渲染。
   *
   * 用 `\n` 连接：Agent 上报的 `text` 是**不含行尾换行**的单行内容，
   * 直接 `join('')` 会把所有行粘成一整行，用户复制日志时全乱。
   * 同时去掉每行自身的尾随换行，避免出现空行夹杂。
   */
  const plainLog = computed(() =>
    output.value.map((c) => stripAnsi(c.text).replace(/\n+$/, '')).join('\n')
  )

  function markDropped(reason: string): void {
    dropped.value = true
    if (!droppedReasons.value.includes(reason)) {
      droppedReasons.value = [...droppedReasons.value, reason]
    }
  }

  function clearTimer(): void {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  /** 停止轮询，**不影响服务器上的任务** */
  function stopPoll(): void {
    generation += 1
    clearTimer()
  }

  /**
   * 关闭弹窗时的收尾。
   *
   * 和 `dispose()` 的区别：**不把 disposed 置真**。
   * 组件卸载才用 dispose；关窗只是停止轮询，之后还要能 `resume()` 接着看，
   * 如果关窗就 disposed，重开时 resume 会被立刻挡回去（读不到任何东西）。
   */
  function pause(): void {
    stopPoll()
  }

  /**
   * 校验响应是否属于当前编排。
   *
   * **先查编排名**：首次响应（还没有 current）时也要校验 —— 否则串项目时
   * 第一份响应会被无条件接受，后面的校验就都建立在错误基础上了。
   * 只有 task_id 的比较需要 current 存在。
   */
  function belongsToCurrent(next: StackPullTask): boolean {
    if (options.expectedName !== undefined && next.name !== '' && next.name !== options.expectedName) {
      return false
    }
    const current = task.value
    if (current === null) return true
    return next.task_id === current.task_id
  }

  /** 检测 seq 缺口：中间有事件丢了，界面要提示而不是当成连续日志 */
  function checkSeqGap(incoming: readonly { seq: number }[]): void {
    for (const e of incoming) {
      if (maxSeenSeq > 0 && e.seq > maxSeenSeq + 1) markDropped('gap')
      if (e.seq > maxSeenSeq) maxSeenSeq = e.seq
    }
  }

  function applySnapshot(next: StackPullTask): void {
    checkSeqGap(next.output)
    task.value = next
    const merged = mergeOutput(output.value, next.output)
    output.value = merged.chunks
    if (merged.dropped) markDropped('trimmed')
    // 后端声明输出被截断，同样要提示——这条之前漏了
    if (next.truncated) markDropped('server')
    // 游标单调：回退会让下一轮重复读同一段，甚至死循环
    if (next.next_cursor >= maxCursor) maxCursor = next.next_cursor
    interrupted.value = false
    uncertain.value = false
    permanentError.value = false
    backoffAttempt = 0
  }

  /**
   * 调度下一次读取（`generation` 是 stale guard，丢弃换代后的旧回调）。
   */
  function schedule(backoff = 0): void {
    clearTimer()
    const myGen = generation
    const delay = backoff > 0 ? backoff : POLL_INTERVAL_MS
    timer = setTimeout(() => {
      void pollOnce(myGen)
    }, delay)
  }

  async function pollOnce(myGen: number): Promise<void> {
    if (disposed || myGen !== generation) return
    const current = task.value
    if (current === null) return
    if (isTerminal(current.status)) return
    if (permanentError.value) return

    try {
      // 传单调递增的游标，而不是直接用响应里的值
      const next = await options.fetch(current.task_id, maxCursor)
      if (disposed || myGen !== generation) return
      if (!belongsToCurrent(next)) {
        // 不是当前任务的响应：停掉，避免污染
        stopPoll()
        return
      }
      applySnapshot(next)
      if (isTerminal(next.status)) return
      schedule()
    } catch (err) {
      if (disposed || myGen !== generation) return
      /*
       * 读取失败**不改变任务状态**：我们只是读不到，不是任务失败了。
       */
      interrupted.value = true
      errorText.value = describeReadError(err)

      if (isPermanent(err)) {
        /*
         * 403/404 每秒重试一次只是白刷服务器，且不会自愈。
         * 停下来，让用户手动重试或关掉。
         */
        permanentError.value = true
        clearTimer()
        return
      }
      // 网络类错误退避后自动重试（1s→2s→4s→8s）
      backoffAttempt += 1
      const delay = Math.min(POLL_INTERVAL_MS * 2 ** (backoffAttempt - 1), MAX_BACKOFF_MS)
      schedule(delay)
    }
  }

  /**
   * 是否永久性错误（403/404）。
   *
   * 用结构判断而不是 `instanceof ApiError`：跨模块边界（不同构建产物、
   * data URL 加载的测试模块、多份打包副本）时 instanceof 会失效，
   * 结果就是 403 被当成普通网络错误无限重试——正是要避免的行为。
   * `status` 是数值且 `code` 是字符串，已经足够识别。
   */
  function httpStatus(err: unknown): number | null {
    if (err === null || typeof err !== 'object') return null
    const status = (err as { status?: unknown }).status
    if (typeof status !== 'number' || !Number.isFinite(status)) return null
    return status
  }

  function isPermanent(err: unknown): boolean {
    const status = httpStatus(err)
    return status === 403 || status === 404
  }

  function describeReadError(err: unknown): string {
    const status = httpStatus(err)
    if (status === 403) {
      return '没有权限查看更新镜像的执行日志。该操作需要服务器的操作权限。'
    }
    if (status === 404) {
      return '任务记录已不存在（可能服务器重启或任务已被清理）。'
    }
    if (status === 503) {
      return '服务器当前不可达，正在重试读取任务状态。'
    }
    return '连接中断，正在重试读取任务状态。'
  }

  /**
   * 启动任务（幂等）。
   *
   * 已有任务直接返回：要重跑必须 `reset()`（且只在确认的终态下允许）。
   */
  async function start(): Promise<void> {
    if (disposed || starting.value) return
    if (task.value !== null) return

    starting.value = true
    errorText.value = ''
    uncertain.value = false
    permanentError.value = false
    if (requestId === '') requestId = newRequestId()
    startAttempted = true
    startInFlight = true
    const myGen = ++generation

    try {
      const created = await options.start(requestId)
      startInFlight = false
      if (disposed) return
      /*
       * 关窗造成的 generation 失效：任务**已经在服务器上建好了**。
       *
       * 不能丢弃它——那样重开时就不知道服务器上已有任务，会用同一个
       * request_id 重新"创建"（幂等能救回来，但语义上我们确实拿到了任务）。
       * 正确做法是**把任务收下来**，只是不启动轮询：
       * 用户重开弹窗时 resume() 能直接看到它。
       */
      if (myGen !== generation) {
        applySnapshot(created)
        // 不 schedule()：窗口已关，没人看，轮询只会白烧
        return
      }
      output.value = []
      dropped.value = false
      droppedReasons.value = []
      maxSeenSeq = 0
      maxCursor = 0
      applySnapshot(created)
      if (!isTerminal(created.status)) schedule()
    } catch (err) {
      startInFlight = false
      if (disposed) return
      if (myGen !== generation) {
        uncertain.value = true
        errorText.value = '网络中断，无法确认任务是否已启动。可以重试确认（不会重复启动）。'
        return
      }
      if (isUncertainError(err)) {
        /*
         * 请求可能已到达后端并启动了任务。绝不能换 request_id——
         * 那会绕过幂等再起一个。
         */
        uncertain.value = true
        errorText.value = '网络中断，无法确认任务是否已启动。可以重试确认（不会重复启动）。'
      } else if (httpStatus(err) === 403) {
        errorText.value = '没有权限更新镜像。该操作需要服务器的操作权限。'
        permanentError.value = true
      } else if (httpStatus(err) === 503) {
        errorText.value = '服务器离线，暂时无法更新镜像。'
      } else {
        errorText.value = err instanceof Error ? err.message : '启动更新镜像失败'
      }
    } finally {
      if (!disposed) starting.value = false
    }
  }

  /** 断线后继续读取，或重开后继续看已有任务（GET 只读，安全） */
  async function resume(): Promise<void> {
    const current = task.value
    if (disposed) return
    /*
     * 没有本地任务、但曾经发起过启动请求：可能任务已在服务器上，
     * 用同一个 request_id 重试（幂等返回同一任务），而不是当新任务。
     */
    if (current === null) {
      if (startAttempted) await start()
      return
    }
    if (isTerminal(current.status)) return
    const myGen = ++generation
    interrupted.value = false
    permanentError.value = false
    errorText.value = ''
    try {
      const next = await options.fetch(current.task_id, maxCursor)
      if (disposed || myGen !== generation) return
      if (!belongsToCurrent(next)) {
        stopPoll()
        return
      }
      applySnapshot(next)
      if (!isTerminal(next.status)) schedule()
    } catch (err) {
      if (disposed || myGen !== generation) return
      interrupted.value = true
      errorText.value = describeReadError(err)
      if (isPermanent(err)) {
        permanentError.value = true
        clearTimer()
        return
      }
      schedule()
    }
  }

  /**
   * 清空本地状态，允许开始全新任务。
   * 只在确认的终态下允许：状态未知时清空会换新 `request_id`，
   * 等于放弃幂等，可能拉起第二个任务。
   */
  function reset(): boolean {
    if (task.value !== null && !finished.value) return false
    if (task.value === null && startAttempted) return false
    if (uncertain.value) return false

    stopPoll()
    task.value = null
    output.value = []
    dropped.value = false
    droppedReasons.value = []
    interrupted.value = false
    uncertain.value = false
    permanentError.value = false
    errorText.value = ''
    starting.value = false
    requestId = ''
    startAttempted = false
    startInFlight = false
    maxSeenSeq = 0
    maxCursor = 0
    backoffAttempt = 0
    return true
  }

  /**
   * 彻底停止这个 runner。
   *
   * 组件卸载会调用它；**测试也必须调用**，否则 setTimeout 会留下
   * 未清理的定时器，Node 进程永远不退出（`node --test` 挂住）。
   * 显式公开而不是只依赖 onUnmounted，因为 composable 不一定在组件里用。
   */
  function dispose(): void {
    disposed = true
    stopPoll()
  }

  /*
   * 只有在组件 setup 里才注册卸载钩子。
   *
   * 这个 composable 也可以脱离组件使用（测试、以后可能的非组件场景），
   * 那种情况下 onUnmounted 会打 "no active component instance" 警告。
   * 外面没有实例就只依赖调用方显式 dispose()。
   */
  if (getCurrentInstance() !== null) onUnmounted(dispose)

  return {
    task,
    output,
    plainLog,
    dropped,
    droppedReasons,
    phase,
    hasTask,
    finished,
    starting,
    interrupted,
    uncertain,
    permanentError,
    errorText,
    start,
    resume,
    reset,
    stopPoll,
    pause,
    dispose,
    /** 启动请求是否在途（关窗时用于提示任务可能仍在创建） */
    isStartInFlight: (): boolean => startInFlight,
    /** 是否发起过启动请求（重开时判断用 uncertain 还是 idle） */
    hasStartAttempted: (): boolean => startAttempted,
    peekRequestId: (): string => requestId,
    limits: { maxBytes: MAX_OUTPUT_BYTES, maxEvents: MAX_OUTPUT_EVENTS }
  }
}

/** 生成幂等键 */
function newRequestId(): string {
  const c = globalThis.crypto
  if (typeof c?.randomUUID === 'function') return c.randomUUID()
  return `${String(Date.now())}-${Math.random().toString(36).slice(2)}`
}

/**
 * 判断错误是「网络层不确定」还是服务端的明确回答。
 *
 * 有 HTTP status 说明服务端答过了（403/404/503 语义明确）；
 * 没有 status（fetch failed / 超时）意味着**连响应都没有**，
 * 任务可能已启动，必须复用原 request_id 重试。
 *
 * 同样用结构判断而非 instanceof，理由见 httpStatus。
 */
function isUncertainError(err: unknown): boolean {
  if (err === null || typeof err !== 'object') return true
  const status = (err as { status?: unknown }).status
  return typeof status !== 'number' || !Number.isFinite(status)
}
