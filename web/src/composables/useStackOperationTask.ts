/**
 * 编排操作任务的状态机（通用）
 *
 * 原来只用于「更新镜像」，现在所有编辑动作共用。逻辑与动作无关，
 * 所以只保留一份实现，`useStackPullTask` 是它的薄封装。
 *
 * 只负责状态与时序；纯计算在 `utils/stackOperationOutput.ts`。
 *
 * ## 不变量
 *
 * 1. 一次点击一个任务，`request_id` 首次生成后长期复用；
 * 2. 网络不确定时绝不换 `request_id`（换了就绕过服务端幂等）；
 * 3. 读不到 ≠ 任务失败，只标记 interrupted 并保留输出；
 * 4. 响应必须属于当前任务（防串项目串状态）；
 * 5. 游标只前进；403/404 停止自动轮询，改手动重试；
 * 6. 关窗只 `pause()` 不取消任务，重开 `resume()` 接着读。
 */
import { computed, getCurrentInstance, onUnmounted, ref, shallowRef } from 'vue'
import type { StackOperationTask } from '@/types/stackOperationTask'
import {
  isConfirmedOperationSuccess,
  isOperationTerminal
} from '@/types/stackOperationTask'
import {
  MAX_OUTPUT_BYTES,
  MAX_OUTPUT_EVENTS,
  mergeOutput,
  segmentOutput,
  stripAnsi,
  type OutputChunk
} from '@/utils/stackOperationOutput'

export const POLL_INTERVAL_MS = 1000
export const MAX_BACKOFF_MS = 8000

export type OperationPhase =
  | 'idle'
  | 'starting'
  | 'queued'
  | 'running'
  | 'done'
  | 'failed'
  | 'interrupted'
  | 'blocked'

export interface StackOperationRunnerOptions {
  /** 启动任务（只收 requestId，其余参数由调用方闭包捕获） */
  start: (requestId: string) => Promise<StackOperationTask>
  /** 读取任务；after 是游标 */
  fetch: (taskId: string, after: number) => Promise<StackOperationTask>
  /** 期望的编排名，用于校验响应身份 */
  expectedName?: string
  /** 期望的动作，用于校验响应身份（防串动作时把别的日志当成自己的） */
  expectedAction?: StackOperationTask['action']
  /**
   * 本次请求是否会把配置保存到 Hub（up + content，或 create）。
   *
   * 只影响 503 的文案与分流：保存类请求 503 通常是「已保存但没执行」，
   * 其余动作 503 只是「连不上，不知道有没有跑起来」，不能一概说成已保存。
   */
  savesConfig?: boolean
}

export function useStackOperationTask(options: StackOperationRunnerOptions) {
  const task = shallowRef<StackOperationTask | null>(null)
  const output = shallowRef<OutputChunk[]>([])
  const dropped = ref(false)
  const droppedReasons = ref<string[]>([])
  const interrupted = ref(false)
  const starting = ref(false)
  const errorText = ref('')
  const uncertain = ref(false)
  const permanentError = ref(false)
  /**
   * 配置已保存但尚未执行（Hub 返回 503 且 Agent 离线）。
   *
   * 这不是普通失败：内容已经落到 Hub 了，界面必须保留草稿，
   * 否则用户刚写的 YAML 白写。可以同请求重试执行。
   */
  const savedNotDeployed = ref(false)

  let requestId = ''
  let timer: ReturnType<typeof setTimeout> | null = null
  let generation = 0
  let disposed = false
  let backoffAttempt = 0
  let maxSeenSeq = 0
  let maxCursor = 0
  let startInFlight = false
  let startAttempted = false

  const phase = computed<OperationPhase>(() => {
    if (starting.value) return 'starting'
    const t = task.value
    if (t === null) {
      /*
       * 发起过启动请求却没拿到任务：状态未知，不能显示 idle。
       * 服务器上可能已经有任务在跑，显示「尚未开始」会误导用户再点一次。
       */
      return startAttempted ? 'blocked' : 'idle'
    }
    if (interrupted.value) return 'interrupted'
    if (t.status === 'queued') return 'queued'
    if (t.status === 'failed') return 'failed'
    if (t.status === 'succeeded') return isConfirmedOperationSuccess(t) ? 'done' : 'failed'
    if (t.status === 'unknown') return 'blocked'
    return 'running'
  })

  const hasTask = computed(() => task.value !== null)
  const finished = computed(() => {
    const t = task.value
    return t !== null && isOperationTerminal(t.status)
  })

  /**
   * 拼接后的纯文本（已脱敏）。
   *
   * 用 `\n` 连接：Agent 上报的 `text` 不含行尾换行，`join('')` 会粘成一行。
   * `command` 事件单独加前缀，让分段在纯文本里也看得出来。
   */
  const plainLog = computed(() =>
    output.value
      .map((c) => {
        const text = stripAnsi(c.text).replace(/\n+$/, '')
        return c.stream === 'command' ? `$ ${text}` : text
      })
      .join('\n')
  )

  /** 按命令分段（多命令动作可逐步展示） */
  const segments = computed(() => segmentOutput(output.value))

  const commandCount = computed(() => output.value.filter((c) => c.stream === 'command').length)

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

  function stopPoll(): void {
    generation += 1
    clearTimer()
  }

  /** 关窗：停止轮询但**不置 disposed**，重开还要能 resume */
  function pause(): void {
    stopPoll()
  }

  function dispose(): void {
    disposed = true
    stopPoll()
  }

  /**
   * 校验响应是否属于当前操作。
   *
   * 三重校验，缺一不可：
   * 1. **编排名**：第一次响应（还没有 current）也要校验。之前 `current === null`
   *    直接放行，串项目时第一份响应会被无条件吞下，之后所有校验都建立在
   *    错误基础上。名字为空也**不算**通过——空名说明后端没给身份信息，
   *    不能假设它是我们的。
   * 2. **动作**：同一个编排可以先后跑 pull 和 rebuild，日志绝不能串。
   * 3. **task_id**：本地已有任务时必须一致，防止旧响应覆盖新任务。
   */
  function belongsToCurrent(next: StackOperationTask): boolean {
    if (options.expectedName !== undefined && next.name !== options.expectedName) {
      return false
    }
    if (options.expectedAction !== undefined && next.action !== options.expectedAction) {
      return false
    }
    const current = task.value
    if (current === null) return true
    return next.task_id === current.task_id
  }

  function checkSeqGap(incoming: readonly { seq: number }[]): void {
    for (const e of incoming) {
      if (maxSeenSeq > 0 && e.seq > maxSeenSeq + 1) markDropped('gap')
      if (e.seq > maxSeenSeq) maxSeenSeq = e.seq
    }
  }

  function applySnapshot(next: StackOperationTask): void {
    checkSeqGap(next.output)
    task.value = next
    const merged = mergeOutput(output.value, next.output)
    output.value = merged.chunks
    if (merged.dropped) markDropped('trimmed')
    if (next.truncated) markDropped('server')
    // 游标单调：回退会让下一轮重复读同一段
    if (next.next_cursor >= maxCursor) maxCursor = next.next_cursor
    interrupted.value = false
    uncertain.value = false
    permanentError.value = false
    savedNotDeployed.value = false
    backoffAttempt = 0
  }

  /** 调度下一次读取（generation 是 stale guard） */
  function schedule(backoff = 0): void {
    clearTimer()
    const myGen = generation
    timer = setTimeout(() => {
      void pollOnce(myGen)
    }, backoff > 0 ? backoff : POLL_INTERVAL_MS)
  }

  async function pollOnce(myGen: number): Promise<void> {
    if (disposed || myGen !== generation) return
    const current = task.value
    if (current === null) return
    if (isOperationTerminal(current.status)) return
    if (permanentError.value) return

    try {
      const next = await options.fetch(current.task_id, maxCursor)
      if (disposed || myGen !== generation) return
      if (!belongsToCurrent(next)) {
        stopPoll()
        return
      }
      applySnapshot(next)
      if (isOperationTerminal(next.status)) return
      schedule()
    } catch (err) {
      if (disposed || myGen !== generation) return
      interrupted.value = true
      errorText.value = describeReadError(err)
      if (isPermanent(err)) {
        // 403/404 每秒重试只是白刷服务器，且不会自愈
        permanentError.value = true
        clearTimer()
        return
      }
      backoffAttempt += 1
      const delay = Math.min(POLL_INTERVAL_MS * 2 ** (backoffAttempt - 1), MAX_BACKOFF_MS)
      schedule(delay)
    }
  }

  function httpStatus(err: unknown): number | null {
    if (err === null || typeof err !== 'object') return null
    const status = (err as { status?: unknown }).status
    return typeof status === 'number' && Number.isFinite(status) ? status : null
  }

  function isPermanent(err: unknown): boolean {
    const status = httpStatus(err)
    return status === 403 || status === 404
  }

  function describeReadError(err: unknown): string {
    const status = httpStatus(err)
    if (status === 403) return '没有权限查看该操作的执行日志。该操作需要服务器的操作权限。'
    if (status === 404) return '任务记录已不存在（可能服务器重启或任务已被清理）。'
    if (status === 503) return '服务器当前不可达，正在重试读取任务状态。'
    return '连接中断，正在重试读取任务状态。'
  }

  /**
   * 启动任务（幂等）。已有任务直接返回：要重跑必须先 `reset()`。
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
       * 关窗造成的 generation 失效：任务**已在服务器上建好**。
       * 不能丢弃——收下它，只是不启动轮询，重开时 resume() 能看到。
       */
      if (myGen !== generation) {
        applySnapshot(created)
        return
      }
      /*
       * 初响应同样要校验身份。
       *
       * 只校验轮询响应是不够的：POST 的响应就是第一份"证据"，
       * 若它属于别的编排/动作（代理错配、缓存串号），直接 apply 会把
       * 别人的命令与日志显示成自己的——比后续校验漏掉更糟。
       */
      if (!belongsToCurrent(created)) {
        errorText.value = '服务器返回的任务不属于当前编排或动作，已停止读取。'
        permanentError.value = true
        return
      }
      output.value = []
      dropped.value = false
      droppedReasons.value = []
      maxSeenSeq = 0
      maxCursor = 0
      applySnapshot(created)
      if (!isOperationTerminal(created.status)) schedule()
    } catch (err) {
      startInFlight = false
      if (disposed) return
      if (myGen !== generation) {
        uncertain.value = true
        errorText.value = '网络中断，无法确认任务是否已启动。可以重试确认（不会重复启动）。'
        return
      }
      if (isUncertainError(err)) {
        uncertain.value = true
        errorText.value = '网络中断，无法确认任务是否已启动。可以重试确认（不会重复启动）。'
      } else if (httpStatus(err) === 403) {
        errorText.value = '没有权限执行该操作。该操作需要服务器的操作权限。'
        permanentError.value = true
      } else if (httpStatus(err) === 409) {
        /*
         * 乐观锁冲突：服务器上的配置已被别人改过。
         *
         * 绝不能拿本地内容覆盖——那会抹掉别人的修改。
         * 明确告诉用户先把自己的修改复制出来，再重新加载。
         */
        permanentError.value = true
        errorText.value = '配置已被他人修改，本次内容未提交。请先复制当前修改，再重新加载配置。'
      } else if (httpStatus(err) === 503) {
        if (options.savesConfig === true) {
          /*
           * 保存类请求（up+content / create）：Hub 先持久化配置再下发，
           * 503 通常意味着配置已入库但执行没成。必须明确告诉用户
           * "已保存"，否则他会以为刚写的 YAML 白写了而重新编辑一遍。
           */
          savedNotDeployed.value = true
          errorText.value =
            '配置已保存到 Hub，但暂时无法确认执行状态。可以重试确认（不会重复保存）。'
        } else {
          /*
           * 其它动作（stop/restart/pull/rebuild/backup/remove）不保存配置。
           * 503 可能是 Agent 超时、任务其实已被受理——这只是"不知道"，
           * 不是"没执行"。说成未执行会误导用户以为要再做一次。
           */
          uncertain.value = true
          errorText.value =
            '无法确认操作是否已执行。可以重试确认（同一个任务，不会重复执行）。'
        }
        permanentError.value = true
      } else {
        errorText.value = err instanceof Error ? err.message : '操作启动失败'
      }
    } finally {
      if (!disposed) starting.value = false
    }
  }

  /** 断线后继续读取，或重开后继续看已有任务（GET 只读，安全） */
  async function resume(): Promise<void> {
    if (disposed) return
    const current = task.value
    if (current === null) {
      // 没有本地任务但曾发起过启动：可能服务器已有任务，同 request_id 重试
      if (startAttempted) await start()
      return
    }
    if (isOperationTerminal(current.status)) return
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
      if (!isOperationTerminal(next.status)) schedule()
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
    savedNotDeployed.value = false
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

  /*
   * 只有在组件 setup 里才注册卸载钩子；脱离组件使用时由调用方显式 dispose()，
   * 否则会打 "no active component instance" 警告。
   */
  if (getCurrentInstance() !== null) onUnmounted(dispose)

  return {
    task,
    output,
    plainLog,
    segments,
    commandCount,
    dropped,
    droppedReasons,
    phase,
    hasTask,
    finished,
    starting,
    interrupted,
    uncertain,
    permanentError,
    savedNotDeployed,
    errorText,
    start,
    resume,
    reset,
    stopPoll,
    pause,
    dispose,
    isStartInFlight: (): boolean => startInFlight,
    hasStartAttempted: (): boolean => startAttempted,
    peekRequestId: (): string => requestId,
    limits: { maxBytes: MAX_OUTPUT_BYTES, maxEvents: MAX_OUTPUT_EVENTS }
  }
}

function newRequestId(): string {
  const c = globalThis.crypto
  if (typeof c?.randomUUID === 'function') return c.randomUUID()
  return `${String(Date.now())}-${Math.random().toString(36).slice(2)}`
}

/**
 * 判断错误是「网络层不确定」还是服务端明确回答。
 *
 * 有 HTTP status 说明服务端答过了；没有 status（fetch failed/超时）
 * 意味着连响应都没有，任务可能已启动，必须复用原 request_id。
 */
function isUncertainError(err: unknown): boolean {
  if (err === null || typeof err !== 'object') return true
  const status = (err as { status?: unknown }).status
  return typeof status !== 'number' || !Number.isFinite(status)
}
