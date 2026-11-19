/**
 * 静默同步节流器
 *
 * ## 解决什么问题
 *
 * 「服务器在线状态」这类轻量同步会被多个入口同时触发：页面挂载、布局切换、
 * 自动刷新轮询、标签页从后台切回。如果每处都直接发请求，同一份数据会被
 * 并发请求打满——后端卡顿时反而更容易堆积。
 *
 * 这里收敛两件事：
 * 1. **在途去重**：同一时刻只允许一个请求，其他调用者复用同一个 Promise；
 * 2. **最小间隔节流**：距上次成功同步不足 minIntervalMs 时直接跳过。
 *
 * ## 为什么失败后不推进节流时间
 *
 * 失败（超时、502、Hub 正好在重启）时若也把时间戳更新为「现在」，下一次重试
 * 要再等满一个间隔——而 Hub 刚恢复的几秒恰恰最需要立刻刷新状态。因此只有
 * **成功**才推进时间戳，失败允许下一轮立即重试；请求频率仍由调用方
 * （usePolling 的间隔与连续失败退避）兜住，不会变成忙等。
 *
 * ## 为什么抽成纯函数
 *
 * 与 Vue / Pinia 无关，时间源可注入，因此能直接单测「并发去重、节流跳过、
 * 失败可重试」这三条边界，不必启动组件或连真实后端。
 */

/** 默认最小同步间隔：与自动刷新轮询同量级，足够避免重复请求 */
export const SYNC_MIN_INTERVAL_MS = 3000

export interface ThrottledSyncOptions {
  /** 真正执行同步；抛错视为失败 */
  run: () => Promise<void>
  /** 默认最小间隔（毫秒）；<= 0 表示不节流 */
  minIntervalMs?: number
  /** 时间源，默认 Date.now；测试注入固定时钟，避免真实等待 */
  now?: () => number
}

export interface ThrottledSync {
  /**
   * 触发一次同步。
   *
   * @param minIntervalMs 本次覆盖默认最小间隔；传 0 表示强制同步（首次校准用）
   * @returns 本次调用后状态是否已同步成功。节流跳过时返回 true
   *          （上一次刚成功，状态仍然可信）；本次同步失败返回 false
   */
  trigger: (minIntervalMs?: number) => Promise<boolean>
}

/**
 * 创建节流器。
 *
 * 边界：节流判断发生在「能否开始一次新请求」这一步，已经开始的请求不会被
 * 后续调用打断或取消；调用方无需关心并发。
 */
export function createThrottledSync(options: ThrottledSyncOptions): ThrottledSync {
  const minGap = options.minIntervalMs ?? SYNC_MIN_INTERVAL_MS
  const now = options.now ?? (() => Date.now())

  /** 最近一次成功同步的时间戳；0 表示从未成功过 */
  let lastSuccessAt = 0
  /** 在途同步；非 null 时其他调用者直接复用，不重复发请求 */
  let inflight: Promise<boolean> | null = null

  async function trigger(minIntervalMs?: number): Promise<boolean> {
    if (inflight !== null) return inflight

    const gap = minIntervalMs ?? minGap
    /*
     * lastSuccessAt === 0 表示从未成功过，此时不节流：
     * 页面刚打开、上一次同步失败、或 Hub 刚重启，都需要立刻拿到真实状态。
     */
    if (gap > 0 && lastSuccessAt !== 0 && now() - lastSuccessAt < gap) return true

    /*
     * 必须先建 Promise 再把 inflight 赋给它，不能在赋值表达式里写
     * `inflight = (async () => { ... inflight = null ... })()`：
     * 同步完成的 run() 会在赋值之前把 inflight 置 null，随后赋值又把已完成的
     * Promise 挂回去，导致后续调用一直复用它、再也不真正同步。
     */
    const task = (async () => {
      try {
        await options.run()
        lastSuccessAt = now()
        return true
      } catch {
        // 失败不推进时间戳：Hub 恢复后的下一轮必须能立即重试
        return false
      } finally {
        inflight = null
      }
    })()
    inflight = task
    return task
  }

  return { trigger }
}
