/**
 * 编排操作任务（启动/停止/重启/更新/重建/保存部署/备份/删除）的类型
 *
 * ## 和 pull-tasks 的关系
 *
 * 原来只有「更新镜像」有实时输出。现在所有编辑动作统一走同一套任务模型：
 * 后端跑真实命令、前端轮询增量输出。契约形状基本一致，只多了 `action`
 * 与 `command` 流，所以这里是**泛化**而非新写一套。
 *
 * `types/stackPullTask.ts` 仍然保留（旧接口兼容），但它现在的字段是这里
 * 的子集，实现上转调本文件，不复制逻辑。
 *
 * ## 契约
 *
 * POST /api/agents/:id/stacks/:name/operation-tasks
 *   { request_id, action, remove_volumes?, include_volumes?, content?, env_content?, expected_config_hash? }
 * GET  /api/agents/:id/stacks/:name/operation-tasks/:taskID?after=N
 *   → { task: <同下> + action }
 */

/**
 * 编辑动作。
 *
 * 与后端 switch 的取值一致（`up/stop/restart/pull/rebuild/remove/backup`）。
 * 注意 `start` 在前端叫「启动」但发的是 `up`，`recreate` 叫「重建」但发 `rebuild`——
 * 翻译只在这一处，写错就是运行时「不支持的动作」。
 */
export type StackOperationAction = 'up' | 'stop' | 'restart' | 'pull' | 'rebuild' | 'remove' | 'backup'

/** 所有动作的固定顺序，用于按钮渲染与测试断言 */
export const STACK_OPERATION_ACTIONS: readonly StackOperationAction[] = [
  'up',
  'stop',
  'restart',
  'pull',
  'rebuild',
  'backup',
  'remove'
]

/**
 * 任务状态。
 *
 * `interrupted` 是**前端独有**的本地状态，后端不会返回：
 * 表示「和 Hub 的连接断了，不知道任务现在怎么样」。
 * 断线既不是成功也不是失败，必须独立成一种状态，否则用户会误判。
 */
export type StackOperationStatus =
  | 'queued'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'unknown'
  | 'interrupted'

const KNOWN_STATUS: readonly string[] = ['queued', 'running', 'succeeded', 'failed']

/**
 * 归一化状态。
 *
 * 不认识的值一律归 `unknown`，**绝不**乐观当成 succeeded——
 * 后端将来加 `canceled` 时，旧前端不能把它显示成「完成」。
 */
export function normalizeOperationStatus(raw: unknown): StackOperationStatus {
  if (typeof raw !== 'string') return 'unknown'
  const s = raw.trim().toLowerCase()
  if (KNOWN_STATUS.includes(s)) return s as StackOperationStatus
  if (s === 'interrupted') return 'interrupted'
  return 'unknown'
}

/** 任务是否已经结束（成功或失败都算结束） */
export function isOperationTerminal(status: StackOperationStatus): boolean {
  return status === 'succeeded' || status === 'failed'
}

/**
 * 输出片段。
 *
 * `command` 流是 Agent 在**每条真实命令执行前**发出的事件，
 * 用它把日志分成「第 1 步、第 2 步…」，用户能看出多命令是在逐步执行。
 *
 * `stage` 流是**没有单一对应命令**的阶段说明（如备份时复制文件），
 * 只是进度文字，不能显示成 `$ 命令`。
 */
export interface StackOperationEvent {
  seq: number
  stream: 'stdout' | 'stderr' | 'command' | 'stage' | 'other'
  text: string
}

/** 任务体 */
export interface StackOperationTask {
  task_id: string
  /** 编排名 */
  name: string
  /** 本次执行的动作 */
  action: StackOperationAction | 'unknown'
  /** 当前正在执行（或最后执行）的命令，argv 拼接，只读展示 */
  command: string
  /** 命令的实际工作目录（Agent 上报；旧版可能为空） */
  working_dir: string
  status: StackOperationStatus
  output: StackOperationEvent[]
  /** 下次增量读取的游标 */
  next_cursor: number
  /** 输出是否已被后端截断 */
  truncated: boolean
  /**
   * 退出码；**只在终态出现**（后端 `omitempty`）。
   * `0` 是有效值，不能用 `if (exit_code)` 判断。
   */
  exit_code: number | null
  /** 失败原因；可选 */
  error: string
  /**
   * 备份产物的相对路径（仅 action === 'backup' 且成功时有值）。
   * 界面上展示给用户，让他知道文件落在哪里。
   */
  backup_path: string
  /**
   * 删除动作：Hub 是否真的删掉了编排记录。
   *
   * 只有 Hub 的条件删除成功时它才为 true，且此时 `status` 也一定是 succeeded；
   * 删除失败时 Hub 会返回 `status:'failed'` + `error` + `exit_code:-1`。
   * 所以「看到 succeeded」等价于「记录已删除」，但仍以此字段为准——
   * 这是 Hub 给出的明确结论，不是前端推断。
   */
  record_removed: boolean
  /**
   * 保存成功后的配置版本号。
   *
   * 编辑页用它刷新本地 `version`，避免拿着旧版本重复保存（会撞乐观锁）。
   * Hub **不**在任务里回传配置正文或密钥，前端要重新 GET 才能看到新内容。
   */
  stack_version: number | null
  started_at: string
  finished_at: string
}

export interface StackOperationTaskResponse {
  task: StackOperationTask
}

/**
 * 启动任务的请求体。
 *
 * `request_id` 是幂等键：同一个 id 重复提交，后端应返回同一个任务。
 * 必须在**一次用户点击**里生成，重试时复用。
 */
export interface StartStackOperationRequest {
  request_id: string
  action: StackOperationAction
  /** 删除时是否同时删除数据卷（永久丢失） */
  remove_volumes?: boolean
  /** 备份时是否包含数据卷 */
  include_volumes?: boolean
  /** 保存并部署 / 新建时的 compose 内容 */
  content?: string
  /**
   * 保存并部署 / 新建时的 .env 内容。
   *
   * 语义沿用 Hub：**不传**表示保留原文件；传空串表示清空。
   * 用 `undefined` 而非 `''` 表达"不改"。
   */
  env_content?: string
  /**
   * 编辑已有编排时**必填**的乐观锁版本号。
   *
   * 这是主并发保护：拿着读到的旧版本提交，Hub 会拒绝，避免覆盖他人改动。
   * **不能**用 `expected_config_hash` 代替——那是额外的二次校验，
   * 不是版本锁的替代品。
   */
  version?: number
  /** 可选的额外校验：提交内容必须与这个哈希一致 */
  expected_config_hash?: string
  /**
   * 新建编排时置 true。
   *
   * 新建走同一个 POST 路径，由这个标记区分——此时不需要 `version`
   * （还没有已存在的记录），但要求 manage 权限。
   */
  create?: boolean
}

function normalizeAction(raw: unknown): StackOperationAction | 'unknown' {
  if (typeof raw !== 'string') return 'unknown'
  const s = raw.trim().toLowerCase()
  return STACK_OPERATION_ACTIONS.includes(s as StackOperationAction)
    ? (s as StackOperationAction)
    : 'unknown'
}

function normalizeStream(raw: unknown): StackOperationEvent['stream'] {
  if (typeof raw !== 'string') return 'other'
  const s = raw.trim().toLowerCase()
  if (s === 'stdout' || s === 'stderr' || s === 'command' || s === 'stage') {
    return s
  }
  return 'other'
}

function finiteNumber(value: unknown, fallback: number): number {
  return typeof value === 'number' && Number.isFinite(value) ? value : fallback
}

function normalizeEvent(raw: unknown): StackOperationEvent | null {
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
 * 字段缺失不能直接信：`exit_code` 缺失 ≠ 0。给保守默认值，
 * 让后续「是否成功」的判断能安全退化。
 */
export function normalizeOperationTask(raw: unknown): StackOperationTask | null {
  if (raw === null || typeof raw !== 'object') return null
  const r = raw as Record<string, unknown>
  const taskId = typeof r.task_id === 'string' ? r.task_id : ''
  if (taskId === '') return null

  const output = Array.isArray(r.output)
    ? r.output.map(normalizeEvent).filter((e): e is StackOperationEvent => e !== null)
    : []

  return {
    task_id: taskId,
    name: typeof r.name === 'string' ? r.name : '',
    action: normalizeAction(r.action),
    command: typeof r.command === 'string' ? r.command : '',
    working_dir: typeof r.working_dir === 'string' ? r.working_dir : '',
    status: normalizeOperationStatus(r.status),
    output,
    next_cursor: finiteNumber(r.next_cursor, 0),
    truncated: r.truncated === true,
    exit_code: typeof r.exit_code === 'number' && Number.isFinite(r.exit_code) ? r.exit_code : null,
    error: typeof r.error === 'string' ? r.error : '',
    backup_path: typeof r.backup_path === 'string' ? r.backup_path : '',
    record_removed: r.record_removed === true,
    stack_version:
      typeof r.stack_version === 'number' && Number.isFinite(r.stack_version)
        ? r.stack_version
        : null,
    started_at: typeof r.started_at === 'string' ? r.started_at : '',
    finished_at: typeof r.finished_at === 'string' ? r.finished_at : ''
  }
}

/**
 * 任务是否成功。
 *
 * **以 status 为准**：后端是状态的唯一权威，「提交了任务」不等于「任务成功」，
 * 前端不做二次推断。
 *
 * 唯一例外：`status === 'succeeded'` 但 `exit_code` 明确非 0——
 * 这是自相矛盾的硬证据（进程退出码说明它失败了），此时按失败显示。
 */
export function isConfirmedOperationSuccess(task: StackOperationTask): boolean {
  if (task.status !== 'succeeded') return false
  if (task.exit_code !== null && task.exit_code !== 0) return false
  return true
}

/**
 * 删除动作是否**真的完成**（可以跳回列表）。
 *
 * 比普通成功更严格：删除是不可逆的，跳走之前必须确认容器真的没了。
 * 除了 status 成功，还要求：
 * - 没有 error 文本；
 * - 退出码为 0（删除必须拿到明确的 0，缺失不算完成）；
 * - 动作确实是 remove。
 *
 * `202 Accepted` 之类"已受理"的响应**不算完成**——任务只是排上了队。
 */
export function isConfirmedRemove(task: StackOperationTask | null): boolean {
  if (task === null) return false
  if (task.action !== 'remove') return false
  if (!isConfirmedOperationSuccess(task)) return false
  if (task.error.trim() !== '') return false
  if (task.exit_code !== 0) return false
  return true
}

/** 每个动作的界面文案（按钮与弹窗标题共用，避免两处写不一样） */
export interface StackOperationMeta {
  /** 按钮/标题用的动作名 */
  label: string
  /** 该动作是否危险（需要二次确认） */
  dangerous: boolean
  /** 弹窗里的说明：这个动作到底会做什么 */
  description: string
}

export const STACK_OPERATION_META: Record<StackOperationAction, StackOperationMeta> = {
  up: {
    label: '启动',
    dangerous: false,
    description: '将按 compose 配置创建并启动容器。'
  },
  stop: {
    label: '停止',
    dangerous: false,
    description: '将停止该编排的所有容器。已停止的容器不会删除，数据卷不受影响。'
  },
  restart: {
    label: '重启',
    dangerous: true,
    description: '将重启该编排的所有容器。重启期间服务会短暂中断。'
  },
  pull: {
    label: '更新镜像',
    dangerous: false,
    description: '只拉取 compose 中声明的镜像，不会自动重建或重启容器。需要生效请另行执行「重建」。'
  },
  rebuild: {
    label: '重建',
    dangerous: true,
    description: '将销毁并重新创建该编排的所有容器。容器内未持久化的数据会丢失，数据卷不受影响。'
  },
  backup: {
    label: '备份',
    dangerous: false,
    description: '将导出该编排的配置与数据卷。'
  },
  remove: {
    label: '删除',
    dangerous: true,
    description: '将停止并移除该编排的所有容器与编排记录。默认保留数据卷。'
  }
}
