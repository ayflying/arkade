/**
 * 保存并部署：判断这次点「保存并部署」是新起一轮，还是只查看已有任务
 *
 * 抽成纯函数是因为这里错一步就会**重复部署**或**让用户以为新内容没生效**：
 *
 * 弹窗是常驻实例（关闭只隐藏，任务与 request_id 必须留着）。
 * 于是出现一个矛盾：
 *
 * - 旧任务还在跑 / 状态未知 → 绝不能开新任务（换 request_id 就是重复下发）；
 * - 旧任务已确认结束，用户又改了内容并点保存 → 必须真的起一个新任务，
 *   否则用户看到的是上一次的日志，会以为自己改的内容已经部署了。
 *
 * 解决办法是「generation key」：只有**确认终态**且确实有新内容时才换 key，
 * 让弹窗重新挂载 → 新 runner → 新 request_id → 一次新 POST。
 * 状态未知时绝不换 key（那就是放弃幂等），也不能因为 props 变化自动换
 * （用户只是编辑，不代表要重新部署）。
 */

/** 点「保存并部署」时的状态 */
export interface DeployRequestState {
  /** 编辑内容与服务端基准不同 */
  dirty: boolean
  /** 是否已有部署任务在执行中 */
  inFlight: boolean
  /**
   * 上一次部署任务是否已达**确认终态**（拿到 succeeded/failed 的任务结果）。
   *
   * 注意：POST 失败且没拿到任务（比如 503）不算终态——
   * 那种情况服务器上可能已有任务，必须同 key 重试，不能换 key。
   */
  settled: boolean
  /** 是否发起过启动请求（区分"从没跑过"和"跑过但状态不明"） */
  attempted: boolean
}

export type DeployRequestDecision =
  /** 只把弹窗打开给用户看已有进度，不发起任何请求 */
  | 'view'
  /** 起一轮全新任务：换 generation、定格新快照、发新 POST */
  | 'new-run'

/**
 * 决策。
 *
 * 注意"有新内容"（dirty）本身**不足以**开新任务：
 * 必须旧任务已确认结束，否则会和进行中/状态未知的任务打架。
 */
export function decideDeployRequest(state: DeployRequestState): DeployRequestDecision {
  // 没改内容：用户只是想再看看窗口
  if (!state.dirty) return 'view'

  // 还在跑：只查看，绝不开第二个任务
  if (state.inFlight) return 'view'

  /*
   * 发起过但没拿到终态结果（网络失败 / 503 / 关窗打断）：
   * 服务器上可能已有任务并可能已经保存了配置，此时换 key 会重复部署。
   * 只允许查看 + 同 key 重试。
   */
  if (state.attempted && !state.settled) return 'view'

  // 从没跑过，或上一次已明确结束 → 用新内容起新一轮
  return 'new-run'
}