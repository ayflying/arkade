/**
 * 编排「更新镜像」任务的类型
 *
 * ## 和镜像管理里的 ImageTask 不是一回事
 *
 * `types/imageTask.ts` 是单镜像拉取（有 phase/percent 那套）。
 * 这里是**编排级**的 pull：跑一条 compose 命令，输出的是原始命令行日志，
 * 没有百分比可言——所以只有 status + 逐行输出，不编造进度。
 *
 * ## 契约来源
 *
 * POST /api/agents/:id/stacks/:name/pull-tasks
 * GET  /api/agents/:id/stacks/:name/pull-tasks/:taskID?after=N
 *
 * 两个接口返回同构任务体，GET 额外带增量 events。
 */

/**
 * 任务状态。
 *
 * `interrupted` 是**前端独有**的本地状态，后端不会返回：
 * 表示「和 Hub 的连接断了，不知道任务现在怎么样」。
 *
 * 为什么必须独立成一种状态：断线既不是成功也不是失败。
 * 归到 succeeded 会让用户以为镜像拉好了，归到 failed 会让人去重跑一遍，
 * 而任务很可能还在正常跑——两种都会造成误判。
 */
export type StackPullStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'unknown' | 'interrupted'

/** 后端可能返回、我们认识的状态值 */
const KNOWN_STATUS: readonly string[] = ['queued', 'running', 'succeeded', 'failed']

/**
 * 归一化状态。
 *
 * 不认识的值一律归 `unknown`，**绝不**乐观当成 succeeded——
 * 后端将来加一个 `canceled` 时，旧前端不能把它显示成「完成」。
 */
export function normalizePullStatus(raw: unknown): StackPullStatus {
  if (typeof raw !== 'string') return 'unknown'
  const s = raw.trim().toLowerCase()
  if (KNOWN_STATUS.includes(s)) return s as StackPullStatus
  if (s === 'interrupted') return 'interrupted'
  return 'unknown'
}

/** 任务是否已经结束（成功或失败都算结束） */
export function isTerminal(status: StackPullStatus): boolean {
  return status === 'succeeded' || status === 'failed'
}

/** 输出的一个片段 */
export interface StackPullEvent {
  /** 序号，用于增量合并与去重 */
  seq: number
  /** 输出流；未知值归 stdout 以外的 'other' */
  stream: 'stdout' | 'stderr' | 'other'
  text: string
}

/** 后端返回的任务体 */
export interface StackPullTask {
  task_id: string
  /** 编排名 */
  name: string
  /** 实际执行的命令（argv 拼接，只读展示；前端不提供任何输入） */
  command: string
  /**
   * 命令的实际工作目录。
   *
   * 由 Agent 上报，**不是**用户可以指定的路径——前端只在界面上展示，
   * 绝不能让人以为可以在这里填任意目录去执行。旧版 Agent 没有这个字段，
   * 归一化成空串，界面此时不显示这一行。
   */
  working_dir: string
  status: StackPullStatus
  output: StackPullEvent[]
  /** 下次增量读取的游标 */
  next_cursor: number
  /** 输出是否已被截断（后端侧） */
  truncated: boolean
  /**
   * 退出码；**只在终态出现**（后端 `omitempty`），未结束时为 null。
   * `0` 是有效值，绝不能用 `if (exit_code)` 判断——那会把成功当失败。
   */
  exit_code: number | null
  /** 失败原因；可选，空串表示没有 */
  error: string
  /** 开始时间（RFC3339；可省略） */
  started_at: string
  /** 结束时间（RFC3339；可省略，未结束时通常没有） */
  finished_at: string
}

export interface StackPullTaskResponse {
  task: StackPullTask
}

/**
 * 启动任务的请求体。
 *
 * `request_id` 是幂等键：同一个 id 重复提交，后端应返回同一个任务而不是新起一个。
 * 所以它必须在**一次用户点击**里生成，重试时复用——
 * 每次重试都换新 id 就等于放弃了幂等，可能拉出两个并发任务。
 */
export interface StartStackPullRequest {
  request_id: string
}

/** 归一化 stream 取值 */
function normalizeStream(raw: unknown): StackPullEvent['stream'] {
  if (typeof raw !== 'string') return 'other'
  const s = raw.trim().toLowerCase()
  if (s === 'stdout' || s === 'stderr') return s
  // 某些实现用数字或别的名字；不是我们认识的就不假装是 stdout
  return 'other'
}

/** 取一个有限数字，非数字返回兜底值 */
function finiteNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

/** 归一化单条事件；seq 非法的丢弃（无法去重） */
function normalizeEvent(raw: unknown): StackPullEvent | null {
  if (raw === null || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const seq = r.seq
  if (typeof seq !== 'number' || !Number.isFinite(seq)) return null
  return {
    seq,
    stream: normalizeStream(r.stream),
    text: typeof r.text === 'string' ? r.text : ''
  }
}

/**
 * 归一化任务体。
 *
 * 字段缺失不能直接信：`exit_code` 缺失 ≠ 0，`truncated` 缺失 ≠ false 之外的猜测。
 * 这里给保守默认值，让它后面的判断（是否成功）能安全退化。
 */
export function normalizePullTask(raw: unknown): StackPullTask | null {
  if (raw === null || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const taskId = typeof r.task_id === 'string' ? r.task_id : ''
  if (taskId === '') return null

  const output = Array.isArray(r.output)
    ? r.output.map(normalizeEvent).filter((e): e is StackPullEvent => e !== null)
    : []

  return {
    task_id: taskId,
    name: typeof r.name === 'string' ? r.name : '',
    command: typeof r.command === 'string' ? r.command : '',
    // 旧版 Agent 不报这个字段；缺省空串，界面据此隐藏「工作目录」
    working_dir: typeof r.working_dir === 'string' ? r.working_dir : '',
    status: normalizePullStatus(r.status),
    output,
    next_cursor: finiteNumber(r.next_cursor, 0),
    truncated: r.truncated === true,
    // 关键：只有真的是数字才认，null/undefined 都保持 null（= 未知/未结束）
    exit_code: typeof r.exit_code === 'number' && Number.isFinite(r.exit_code) ? r.exit_code : null,
    error: typeof r.error === 'string' ? r.error : '',
    started_at: typeof r.started_at === 'string' ? r.started_at : '',
    finished_at: typeof r.finished_at === 'string' ? r.finished_at : ''
  }
}

/**
 * 任务是否成功。
 *
 * **以 status 为准**：后端是任务状态的唯一权威，审计记录里"提交了任务"
 * 不等于"任务成功"，前端不做二次推断。
 *
 * 只有一个例外——`status === 'succeeded'` 但 `exit_code` 明确非 0，
 * 这是**自相矛盾的硬证据**（进程退出码说明它失败了），此时按失败显示。
 * 没有 exit_code（omitempty 未给）时完全信 status。
 */
export function isConfirmedSuccess(task: StackPullTask): boolean {
  if (task.status !== 'succeeded') return false
  if (task.exit_code !== null && task.exit_code !== 0) return false
  return true
}
