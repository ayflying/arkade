/**
 * 编排列表统一模型测试
 *
 * 纯 Node，无框架。跑法：node tests/unified-list.contract.test.mjs
 *
 * ## 这份测试守的是什么
 *
 * 两张表合并成一张，最容易出的错是**悄悄丢数据**：
 * - 同名项目两边都有时，漏掉一边的信息；
 * - 排序时把「未知容器数」当成 0，让停摆的项目混进运行中的队伍；
 * - 合并函数原地 sort/push，污染页面 ref，导致刷新后行序诡异。
 *
 * 所以这里的断言全部打在**真实输入 → 真实输出**上，不 mock 数据结构。
 */
import {
  buildStackRows,
  computeStats,
  filterRows,
  isRowRunning
} from '../src/utils/stackListModel.ts'

let passed = 0
const failures = []

function check(name, ok, detail) {
  if (ok) {
    passed += 1
  } else {
    failures.push(detail === undefined ? name : `${name} —— ${detail}`)
  }
}

/** 造一条已纳管编排记录（形状对齐 types/stack.ts 的 StackSummary） */
function managed(over) {
  return {
    name: 'app',
    status: 'running',
    config_hash: 'h1',
    remote_hash: 'h1',
    drift: false,
    services: ['web'],
    running: 1,
    total: 1,
    version: 1,
    updated_at: 1700000000,
    synced_at: 1700000000,
    agent_id: 'ag-1',
    ...over
  }
}

/** 造一条外部项目记录（对齐 types/stack.ts 的 ExternalStack） */
function external(over) {
  return {
    name: 'side-project',
    status: 'running',
    services: ['db'],
    running: 1,
    total: 1,
    managed: false,
    ...over
  }
}

/* ───────── 基本合并 ───────── */

{
  const rows = buildStackRows([managed({ name: 'a' })], [external({ name: 'b' })])
  check('合并: 两边各有一条 → 两行', rows.length === 2, String(rows.length))
  check(
    '合并: 已纳管行的 source 为 arkade',
    rows.find((r) => r.name === 'a').source === 'arkade'
  )
  check(
    '合并: 外部行的 source 为 server',
    rows.find((r) => r.name === 'b').source === 'server'
  )
  check(
    '合并: 外部行 regState 为 unregistered',
    rows.find((r) => r.name === 'b').regState === 'unregistered'
  )
}

/* ───────── 同名以已纳管为准 ───────── */

{
  const rows = buildStackRows(
    [managed({ name: 'same', running: 2, total: 3 })],
    [external({ name: 'same', running: 9, total: 9 })]
  )
  check('同名: 只保留一行', rows.length === 1, String(rows.length))
  check('同名: 采用已纳管的数据', rows[0].running === 2 && rows[0].total === 3, '外部数据不该覆盖 Hub')
  check('同名: source 为 arkade', rows[0].source === 'arkade')
  check('同名: 仍保留对应的外部记录用于补充上下文', rows[0].external !== null)
}

/* ───────── 登记状态 ───────── */

{
  const synced = buildStackRows([managed({ synced_at: 1700000000 })], [])[0]
  check('登记: synced_at > 0 → synced', synced.regState === 'synced')
  check('登记: 已同步时 reason 为空', synced.regReason === '')

  const unsynced = buildStackRows(
    [managed({ synced_at: 0, sync_hint: '目标服务器离线' })],
    []
  )[0]
  check('登记: synced_at = 0 → unsynced', unsynced.regState === 'unsynced')
  check('登记: 保留后端给的未同步原因', unsynced.regReason === '目标服务器离线', unsynced.regReason)

  /*
   * Hub 重启会丢掉内存里的 sync_hint，但记录仍在（synced_at 还是 0）。
   * 此时必须退回通用文案，不能因为"没有原因"就当成已同步。
   */
  const noHint = buildStackRows([managed({ synced_at: 0, sync_hint: undefined })], [])[0]
  check('登记: 没有 hint 时仍有通用原因', noHint.regReason !== '', '空原因会让人以为没问题')
  check('登记: 没有 hint 也不谎称已同步', noHint.regState === 'unsynced')
}

/* ───────── 未知不等于 0 ───────── */

{
  const rows = buildStackRows([managed({ running: null, total: null, status: 'running' })], [])
  check('未知: running/total 保持 null', rows[0].running === null && rows[0].total === null)
  check(
    '未知: 计数未知时退回状态字符串判断运行中',
    isRowRunning(rows[0]) === true,
    'status 是 running 应算运行中'
  )

  const zero = buildStackRows([managed({ running: 0, total: 3, status: 'stopped' })], [])[0]
  check('零: running=0 不算运行中', isRowRunning(zero) === false)
}

/* ───────── 排序 ───────── */

{
  const rows = buildStackRows(
    [
      managed({ name: 'z-stopped', running: 0, total: 1 }),
      managed({ name: 'b-running', running: 1, total: 1 }),
      managed({ name: 'a-stopped', running: 0, total: 1 })
    ],
    []
  )
  check('排序: 运行中的排在最前', rows[0].name === 'b-running', rows.map((r) => r.name).join(','))
  check(
    '排序: 同档内按名称升序',
    rows[1].name === 'a-stopped' && rows[2].name === 'z-stopped',
    rows.map((r) => r.name).join(',')
  )
}

/* ───────── 不修改入参 ───────── */

{
  const stacks = [managed({ name: 'z' }), managed({ name: 'a' })]
  const externals = [external({ name: 'm' })]
  const stacksSnapshot = JSON.stringify(stacks)
  const externalsSnapshot = JSON.stringify(externals)
  const stacksRef = stacks.slice()
  const externalsRef = externals.slice()

  buildStackRows(stacks, externals)

  check('纯函数: 不修改已纳管数组内容', JSON.stringify(stacks) === stacksSnapshot)
  check('纯函数: 不修改外部数组内容', JSON.stringify(externals) === externalsSnapshot)
  check(
    '纯函数: 不重排传入数组',
    stacks.every((s, i) => s === stacksRef[i]),
    '原地 sort 会污染页面 ref'
  )
  check('纯函数: 外部数组引用顺序不变', externals.every((e, i) => e === externalsRef[i]))
}

/* ───────── 重复与降级 ───────── */

{
  const rows = buildStackRows([managed({ name: 'dup' }), managed({ name: 'dup' })], [])
  check('重复: 同名已纳管记录只出一行', rows.length === 1, String(rows.length))

  // 外部项目已出现在已纳管里又被扫描到时，不能重复出现
  const rows2 = buildStackRows([managed({ name: 'x' })], [external({ name: 'x' }), external({ name: 'y' })])
  check('重复: 外部重复项被已纳管吸收', rows2.length === 2, rows2.map((r) => r.name).join(','))
}

/* ───────── 更新时间 ───────── */

{
  const rows = buildStackRows([managed({ updated_at: 1700000000 })], [external({ name: 'ext' })])
  check(
    '时间: 已纳管保留 updated_at',
    rows.find((r) => r.name === 'app').updatedAt === 1700000000
  )
  check(
    '时间: 外部项目 updatedAt 为 0（界面显示 —）',
    rows.find((r) => r.name === 'ext').updatedAt === 0
  )
  const bad = buildStackRows([managed({ updated_at: Number.NaN })], [])[0]
  check('时间: 非法值归一为 0', bad.updatedAt === 0)
}

/* ───────── 统计 ───────── */

{
  const rows = buildStackRows(
    [
      managed({ name: 'm1', running: 1, total: 1 }),
      managed({ name: 'm2', running: 0, total: 1, synced_at: 0 })
    ],
    [external({ name: 'e1' }), external({ name: 'e2' })]
  )
  const stats = computeStats(rows)
  check('统计: 总数正确', stats.total === 4, String(stats.total))
  // m1 运行中 + e1、e2 各有一个在跑 = 3；m2 是 0
  check('统计: 运行中数量正确', stats.running === 3, String(stats.running))
  check('统计: 未登记数量正确', stats.unregistered === 2, String(stats.unregistered))
  check('统计: 未同步数量正确', stats.unsynced === 1, String(stats.unsynced))
}

/* ───────── 筛选 ───────── */

{
  const rows = buildStackRows(
    [managed({ name: 'web-app', services: ['frontend', 'backend'] })],
    [external({ name: 'legacy', services: ['mysql'] })]
  )
  check('筛选: 空关键词返回全部', filterRows(rows, '').length === 2)
  check('筛选: 按名称匹配', filterRows(rows, 'web').length === 1)
  check('筛选: 按服务名匹配已纳管', filterRows(rows, 'frontend').length === 1)
  check('筛选: 按服务名匹配外部项目', filterRows(rows, 'mysql').length === 1)
  check('筛选: 大小写不敏感', filterRows(rows, 'WEB').length === 1)
  check('筛选: 无匹配返回空', filterRows(rows, 'nothing-here').length === 0)
  check('筛选: 不修改入参', rows.length === 2)
}

/* ───────── 空输入 ───────── */

{
  check('空: 两边都空返回空数组', buildStackRows([], []).length === 0)
  const stats = computeStats([])
  check('空: 统计全 0', stats.total === 0 && stats.running === 0)
}

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 编排列表模型测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 编排列表模型测试：${passed} 通过，0 失败`)
