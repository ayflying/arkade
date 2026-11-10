/**
 * 弹窗打开时的入口决策（纯函数）
 *
 * 抽出来是因为这里有真实 bug 风险，而它埋在组件生命周期里最难测：
 *
 * - 首次挂载时 `show` 已经是 true（父级常驻实例 + show 绑定），
 *   若 watcher 不是 immediate，autoStart 永远不触发——用户点了确认却没执行；
 * - 每次重开都重新定格快照，会把「状态未知」那次请求的 payload 改掉，
 *   同一个 request_id 配新内容，Hub 幂等校验返回 409；
 * - 已有任务时误触发 start，等于重复下发一次危险操作。
 *
 * 做成纯函数后可以逐条穷举验证，组件只负责把结果映射成动作。
 */

/** 弹窗相关的任务状态快照（从 runner 取值） */
export interface EntryState {
  /** 是否已拿到任务（task !== null） */
  hasTask: boolean
  /** 任务是否已进入终态 */
  taskFinished: boolean
  /** 是否已经发起过启动请求（不代表拿到任务） */
  startAttempted: boolean
  /** 父级是否要求自动开始（已做过二次确认） */
  autoStart: boolean
  /** autoStart 是否已经触发过 */
  autoStarted: boolean
}

/** 打开弹窗后应该做什么 */
export type EntryAction =
  /** 继续读取已有任务（只读，安全） */
  | 'resume'
  /** 用同一个 request_id 重试确认（不换 key） */
  | 'retry-same-key'
  /** 等用户点按钮才开始 */
  | 'await-user'
  /** 立刻开始（全新任务，需要定格快照） */
  | 'start-new'
  /** 已有终态任务，什么都不做，等用户明确点「重新执行」 */
  | 'do-nothing'

/**
 * 计算打开弹窗时的动作。
 *
 * 顺序很重要：先处理"已有任务"，再处理"发起过但没任务"，
 * 最后才是全新任务。
 */
export function decideEntry(state: EntryState): EntryAction {
  if (state.hasTask) {
    // 未结束 → 接着看；已结束 → 什么都不做（要重跑必须用户明确点）
    return state.taskFinished ? 'do-nothing' : 'resume'
  }

  /*
   * 没有本地任务但发起过启动：状态未知（可能服务器上已有任务在跑）。
   * 绝不能当作全新任务——那会换 request_id，绕过服务端幂等。
   */
  if (state.startAttempted) return 'retry-same-key'

  // 真正的新任务：autoStart 且从没触发过才自动开始，否则等用户点
  if (state.autoStart && !state.autoStarted) return 'start-new'
  return 'await-user'
}

/**
 * 是否需要为本次动作定格一份新快照。
 *
 * 只有「全新任务」需要（开始或等待用户点开始都算）。
 * 已有任务 / 状态未知都必须沿用原快照，
 * 否则同一个 request_id 会带上不同的 payload，Hub 幂等校验会拒绝。
 */
export function shouldSnapshot(action: EntryAction): boolean {
  return action === 'start-new' || action === 'await-user'
}