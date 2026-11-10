/**
 * 编排列表的统一视图模型
 *
 * ## 为什么需要这一层
 *
 * 编排曾经是两张表：一张「已纳管」（来自 Hub 数据库），一张「服务器已有（未纳管）」
 * （来自扫描）。同一个项目在用户眼里就是「一个编排」，被拆成两处后：
 * - 用户在两个地方找同一个项目；
 * - 同名的两边都有时（接管刚完成、或扫描数据滞后），会看到两行；
 * - 想比较「哪个在 Arkade 里有配置」得来回看。
 *
 * 所以合并成一张表：**按名称对齐**，同名以已纳管为准（它是权威数据源），
 * 外部记录里的额外信息（未登记原因）作为补充。
 *
 * ## 这里的纯函数都要能单独测
 *
 * 合并/排序/统计不碰 Vue、不碰 DOM，只进出数组。
 * 列表页只负责渲染，逻辑错了测试能直接抓到。
 */
import type { ExternalStack, StackSummary } from '@/types/stack'

/**
 * 配置来源。
 *
 * - `arkade`：配置存在 Hub 里（已纳管），能从 Arkade 编辑与下发；
 * - `server`：配置在服务器的原目录里，Arkade 只是看得见它。
 *
 * 这不是"好/坏"之分，只是告诉用户改配置时该去哪里。
 */
export type StackConfigSource = 'arkade' | 'server'

/** 登记状态：配置有没有在 Arkade 里留下一份记录 */
export type StackRegState = 'synced' | 'unsynced' | 'unregistered'

/**
 * 表格里的一行：可能是已纳管、也可能是仅外部发现的编排。
 *
 * 刻意做成**一个**类型而不是联合类型：表格列对所有行都一样，
 * 用联合会让每个单元格都要判断类型，反而容易漏。
 */
export interface StackListRow {
  /** 行唯一标识；同名只会有一行，所以直接用名字 */
  key: string
  /** 编排（compose 项目）名 */
  name: string
  /** 配置存在哪 */
  source: StackConfigSource
  /**
   * 运行状态原始值。
   *
   * 已纳管走 `StackState`（含 offline/busy），外部项目后端给的是裸字符串。
   * 这里统一成 string，由状态映射工具决定怎么显示——未知值原样展示，
   * 不假装成已知状态。
   */
  status: string
  /** 运行中的数量；null = 未知（**不是 0**） */
  running: number | null
  /** 总数；null = 未知（**不是 0**） */
  total: number | null
  /** 登记状态 */
  regState: StackRegState
  /** 登记状态的简短原因，用于 tooltip；空串表示没有额外说明 */
  regReason: string
  /** 更新时间（Unix 秒）；0 表示未知（外部项目拿不到） */
  updatedAt: number
  /** 对应的已纳管记录；外部项目为 null */
  managed: StackSummary | null
  /** 对应的外部记录；已纳管项目可能是 null */
  external: ExternalStack | null
}

/** 把 sec 归一成「未知」用 0 表示；负数/NaN 一律视为未知 */
function normalizeTime(value: number | undefined): number {
  if (typeof value !== 'number' || !Number.isFinite(value) || value <= 0) return 0
  return value
}

/**
 * 已纳管项目的登记状态。
 *
 * `synced_at > 0` 才算已同步到服务器。**0 不等于"刚同步"**，它表示从未成功过。
 * 未同步的原因用后端给的 `sync_hint`；没有 hint 时（Hub 重启会把内存里的原因丢掉）
 * 必须退回通用文案，**不能因为拿不到原因就假装同步成功**。
 */
function managedReg(row: StackSummary): { state: StackRegState; reason: string } {
  if (row.synced_at > 0) return { state: 'synced', reason: '' }
  const hint = typeof row.sync_hint === 'string' ? row.sync_hint.trim() : ''
  return {
    state: 'unsynced',
    reason: hint !== '' ? hint : '配置已记录在 Arkade，但尚未下发到服务器'
  }
}

/**
 * 外部项目的登记状态。
 *
 * 到了这里说明它**不在**已纳管列表里，也就是 Arkade 里没有它的配置记录。
 * 自动登记失败/跳过的原因由 `auto_adopt_reason` 给出（可能是整句中文）。
 */
function externalReg(row: ExternalStack): { state: StackRegState; reason: string } {
  const reason = typeof row.auto_adopt_reason === 'string' ? row.auto_adopt_reason.trim() : ''
  return {
    state: 'unregistered',
    reason: reason !== '' ? reason : '配置只存在于服务器的原目录，Arkade 中没有记录'
  }
}

/**
 * 合并已纳管与外部项目，按名称对齐成一张表。
 *
 * ## 合并规则
 *
 * - **同名以已纳管为准**：Hub 数据库是权威的，扫描数据可能滞后几分钟；
 * - 已纳管记录额外保留对应的外部记录，用来补未登记原因之外的上下文；
 * - 只出现在外部列表里的项目 → `source: 'server'`。
 *
 * ## 不修改入参
 *
 * 传入的数组来自 ref，原地 sort/push 会污染页面状态并让刷新行为变得诡异。
 * 这里全程新建对象。
 *
 * ## 排序
 *
 * 运行中优先 → 再按名称。让「有东西在跑的项目」浮到顶部，
 * 这是用户打开这个页面最常想看的；同状态内按名称稳定排列，避免每 5 秒轮询就跳位。
 */
export function buildStackRows(
  stacks: readonly StackSummary[],
  externalStacks: readonly ExternalStack[]
): StackListRow[] {
  const rows: StackListRow[] = []
  const byName = new Map<string, StackSummary>()
  for (const s of stacks) {
    // 后端理论上不会给重复名，真出现时保留第一条，避免表格出现重复 key
    if (!byName.has(s.name)) byName.set(s.name, s)
  }

  const consumed = new Set<string>()

  for (const s of byName.values()) {
    const reg = managedReg(s)
    const matchedExternal = externalStacks.find((e) => e.name === s.name) ?? null
    if (matchedExternal !== null) consumed.add(matchedExternal.name)
    rows.push({
      key: s.name,
      name: s.name,
      source: 'arkade',
      status: s.status,
      running: s.running,
      total: s.total,
      regState: reg.state,
      regReason: reg.reason,
      updatedAt: normalizeTime(s.updated_at),
      managed: s,
      external: matchedExternal
    })
  }

  for (const e of externalStacks) {
    // 同名的已经在已纳管里出现过了，以那边为准
    if (consumed.has(e.name) || byName.has(e.name)) continue
    const reg = externalReg(e)
    rows.push({
      key: e.name,
      name: e.name,
      source: 'server',
      status: e.status,
      running: e.running,
      total: e.total,
      regState: reg.state,
      regReason: reg.reason,
      // 外部项目没有可用的更新时间信息，保持 0 由界面显示「—」
      updatedAt: 0,
      managed: null,
      external: e
    })
  }

  return sortRows(rows)
}

/** 运行中的排在前面；同档按名称，保证轮询刷新时行不跳位 */
function sortRows(rows: StackListRow[]): StackListRow[] {
  return [...rows].sort((a, b) => {
    const aRunning = isRowRunning(a) ? 1 : 0
    const bRunning = isRowRunning(b) ? 1 : 0
    if (aRunning !== bRunning) return bRunning - aRunning
    return a.name.localeCompare(b.name)
  })
}

/**
 * 这一行是否「有东西在跑」。
 *
 * 优先用容器计数：`running > 0` 最直接。
 * 计数未知（null）时才退回状态字符串——注意 `busy` 不算运行中，
 * 它表示正在执行操作、读到的是半写状态。
 */
export function isRowRunning(row: StackListRow): boolean {
  if (typeof row.running === 'number') return row.running > 0
  return row.status === 'running'
}

/** 列表统计：顶部副标题用，避免模板里重复 filter */
export interface StackListStats {
  total: number
  running: number
  unregistered: number
  unsynced: number
}

export function computeStats(rows: readonly StackListRow[]): StackListStats {
  let running = 0
  let unregistered = 0
  let unsynced = 0
  for (const r of rows) {
    if (isRowRunning(r)) running += 1
    if (r.regState === 'unregistered') unregistered += 1
    if (r.regState === 'unsynced') unsynced += 1
  }
  return { total: rows.length, running, unregistered, unsynced }
}

/**
 * 按名称/服务名过滤。
 *
 * 只做「包含」匹配，不引入模糊评分：编排数量通常是个位数，
 * 复杂搜索反而让人猜不到为什么某行没出现。
 * 空关键词返回全部。
 */
export function filterRows(rows: readonly StackListRow[], keyword: string): StackListRow[] {
  const q = keyword.trim().toLowerCase()
  if (q === '') return [...rows]
  return rows.filter((r) => {
    if (r.name.toLowerCase().includes(q)) return true
    const services = r.managed?.services ?? r.external?.services ?? null
    if (services === null) return false
    return services.some((s) => s.toLowerCase().includes(q))
  })
}
