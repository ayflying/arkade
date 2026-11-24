/**
 * 服务器资源的「多选 + 批量删除 + 清理」逻辑
 *
 * 镜像 / 网络 / 存储卷三个页面除了「删什么、怎么判断能删」之外，
 * 多选状态、批量执行的进度、部分失败的汇总**完全一致**。
 * 各写一遍的话，任何一处修复都要改三遍，而且三份实现迟早会走偏
 * （比如一处先刷新后汇总、另一处反过来）。
 *
 * ## 为什么是前端逐个删除，而不是新增批量接口
 *
 * 三个列表接口本来就返回全量数组，删除一个就是一个 DELETE，
 * 逐个发即可。新增批量接口要为中心三条路由、设计「部分失败怎么返回」，
 * 而真正想要的信息——**哪一个没删掉、为什么**——逐个删除天然就有了。
 * 批量接口反而容易把 5 个成功 2 个失败压成一句「批量删除失败」。
 *
 * ## 部分失败必须逐个说清
 *
 * 卷和网络删除会失败在具体对象上（正被容器使用、内置网络、镜像被引用）。
 * 所以这里不使用「失败就中止」：跳过失败项继续删其余的，
 * 最后汇总成「成功 N 个，失败 M 个：<名字> — <原因>」。
 * 中止会让用户重来一遍，还得自己猜是哪一个卡住了。
 */

import { computed, ref, type ComputedRef, type Ref } from 'vue'
import { toastError, toastSuccess } from '@/utils/feedback'

/** 批量操作里单个目标的处理结果 */
export interface BulkOutcome {
  /** 用于提示文案的名字 */
  label: string
  /** 失败原因；空表示成功 */
  error: string
}

export interface UseBulkActionsOptions<T> {
  /** 当前数据（全量，用于「全选」与清理扫描） */
  items: Ref<T[]> | ComputedRef<T[]>
  /** 行的唯一键（与表格 row-key 必须一致，否则选中状态对不上行） */
  keyOf: (item: T) => string
  /** 用户给这一项的可读名字 */
  labelOf: (item: T) => string
  /** 删除一项；抛错即视为失败，错误消息作为原因 */
  removeOne: (item: T) => Promise<void>
  /** 删除完成后重新拉列表 */
  refresh: () => Promise<void>
  /** 一次操作的单位量词，用于提示文案，如「个卷」 */
  unit: string
}

export interface UseBulkActionsReturn {
  /** 已选中的行键 */
  checkedKeys: Ref<string[]>
  /** 有批量操作在跑 */
  busy: Ref<boolean>
  /** 选中数量 */
  selectedCount: ComputedRef<number>
  /** 切换单行 */
  toggleRow: (key: string) => void
  /** 整页全选/取消（只作用于当前页） */
  togglePage: (keys: string[]) => void
  /** 清空选择 */
  clear: () => void
  /**
   * 批量删除已选中的项。
   *
   * @param targets 调用方按选择算出的待删项（已排除不能删的）
   * @param skipped 因不可删被跳过的数量，用于在提示里如实说明
   */
  removeMany: (targets: Array<{ key: string; label: string }>, skipped: number) => Promise<void>
  /**
   * 清理：传入「按规则筛出来的可清理项」，逐项删除并汇总。
   *
   * 与 removeMany 分开是因为语义不同：清理是按规则扫全量、
   * 不看当前选择，提示里也不能提「已选」。
   */
  cleanup: (targets: Array<{ key: string; label: string }>) => Promise<void>
}

/**
 * 把失败清单压成一句可读的提示。
 *
 * 只列前几条并在末尾写「等」，因为失败多时全列会把提示框撑爆；
 * 用户真正需要的是「哪几个、大概为什么」，细节可以再点一次逐个看。
 */
function failureSummary(outcomes: BulkOutcome[]): string {
  const failed = outcomes.filter((o) => o.error !== '')
  if (failed.length === 0) return ''
  const shown = failed.slice(0, 3).map((o) => `${o.label}（${o.error}）`).join('、')
  const rest = failed.length > 3 ? ` 等 ${failed.length} 个` : ''
  return failed.length > 3 ? `${shown}${rest}` : shown
}

export function useBulkActions<T>(options: UseBulkActionsOptions<T>): UseBulkActionsReturn {
  const checkedKeys = ref<string[]>([])
  const busy = ref(false)

  const selectedCount = computed(() => checkedKeys.value.length)

  function toggleRow(key: string): void {
    const idx = checkedKeys.value.indexOf(key)
    if (idx >= 0) checkedKeys.value.splice(idx, 1)
    else checkedKeys.value.push(key)
  }

  /**
   * 整页全选 / 取消。
   *
   * 只动当前页的键：跨页累积选择看似方便，实际很危险——
   * 用户在第 1 页勾了几项、翻到第 3 页再点「全选」，很容易以为
   * 只会删当前页，结果把前面勾的也一起删了。这里保持「选择=当前页」的直觉。
   */
  function togglePage(keys: string[]): void {
    const allSelected = keys.length > 0 && keys.every((k) => checkedKeys.value.includes(k))
    if (allSelected) {
      checkedKeys.value = checkedKeys.value.filter((k) => !keys.includes(k))
      return
    }
    const merged = new Set(checkedKeys.value)
    for (const k of keys) merged.add(k)
    checkedKeys.value = Array.from(merged)
  }

  function clear(): void {
    checkedKeys.value = []
  }

  /**
   * 逐个删除并汇总结果。
   *
   * 不做并发：这些删除在 Agent 侧串行执行更安全（卷删除会动 daemon 状态），
   * 而且并发下第一个失败后难以判断其余是否受影响。
   * 数量通常是几个到几十个，串行的等待可以接受。
   */
  async function runAll(targets: Array<{ key: string; label: string }>): Promise<BulkOutcome[]> {
    const outcomes: BulkOutcome[] = []
    for (const target of targets) {
      try {
        await options.removeOne(findItem(target.key))
        outcomes.push({ label: target.label, error: '' })
      } catch (err) {
        outcomes.push({
          label: target.label,
          error: err instanceof Error && err.message !== '' ? err.message : '删除失败'
        })
      }
    }
    return outcomes
  }

  /** 按行键找回原始数据；找不到说明列表已刷新，按失败处理而不是静默跳过 */
  function findItem(key: string): T {
    const item = options.items.value.find((i) => options.keyOf(i) === key)
    if (item === undefined) {
      throw new Error('列表已变化，请刷新后重试')
    }
    return item
  }

  async function execute(targets: Array<{ key: string; label: string }>): Promise<void> {
    if (busy.value || targets.length === 0) return
    busy.value = true
    try {
      const outcomes = await runAll(targets)
      // 无论成败都以服务端为准重新拉一次：部分失败时本地状态必然不一致
      await options.refresh()
      clear()

      const ok = outcomes.filter((o) => o.error === '').length
      const bad = outcomes.length - ok
      if (bad === 0) {
        toastSuccess(`已删除 ${String(ok)} ${options.unit}`)
        return
      }
      // 全部失败与部分失败分开说：前者是操作没生效，后者是留下了一半现场
      const head = ok === 0
        ? `删除失败（${String(bad)} ${options.unit}）`
        : `成功 ${String(ok)} ${options.unit}，失败 ${String(bad)} 个`
      toastError(`${head}：${failureSummary(outcomes)}`)
    } finally {
      busy.value = false
    }
  }

  return {
    checkedKeys,
    busy,
    selectedCount,
    toggleRow,
    togglePage,
    clear,
    removeMany: async (targets, skipped) => {
      await execute(targets)
      // 被跳过的项单独说一句：用户勾了却没删，必须让他知道不是漏了
      if (skipped > 0) {
        toastError(`另有 ${String(skipped)} ${options.unit}因不能删除已跳过`)
      }
    },
    cleanup: async (targets) => {
      await execute(targets)
    }
  }
}

/**
 * 构造确认框里的对象清单：超过 limit 个就折叠成「等 N 个」。
 *
 * 卷删除不可恢复，用户必须在确认框里看清要删什么；
 * 但 200 个卷全列会把对话框撑成一面墙，反而看不清。
 */
export function describeTargets(labels: string[], limit = 10): string {
  if (labels.length === 0) return ''
  const shown = labels.slice(0, limit).map((l) => `· ${l}`).join('\n')
  const rest = labels.length - limit
  return rest > 0 ? `${shown}\n… 等共 ${String(labels.length)} 个` : shown
}
