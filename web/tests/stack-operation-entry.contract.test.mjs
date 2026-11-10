/**
 * 弹窗入口逻辑测试（**实际执行**决策函数，不是正则匹配源码）
 *
 * 跑法：node tests/stack-operation-entry.contract.test.mjs
 *
 * 这些 bug 只在组件生命周期里显现，纯源码断言看不出问题：
 *
 * 1. 常驻实例 + :show 绑定 → 组件创建时 show 已经是 true。
 *    非 immediate 的 watcher 不会触发，autoStart 等于失效：
 *    用户点了「确认」却没执行。必须 immediate。
 * 2. 每次重开都重新定格快照 → 「状态未知」那次请求的 payload 被改写，
 *    同一个 request_id 配新内容，Hub 幂等校验返回 409。
 * 3. 已有任务时误触发 start → 重复下发危险操作。
 */
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import ts from 'typescript'

const here = dirname(fileURLToPath(import.meta.url))
const SRC_ROOT = join(here, '..', 'src')

async function loadTs(relPath) {
  const src = readFileSync(join(here, '..', relPath), 'utf8')
  const js = ts.transpileModule(src, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 }
  }).outputText
  const rewritten = js.replace(/from\s+['"]([^'"]+)['"]/g, (whole, spec) => {
    if (spec.startsWith('@/')) {
      const target = spec.slice(2)
      return `from '${pathToFileURL(join(SRC_ROOT, target.endsWith('.ts') ? target : `${target}.ts`)).href}'`
    }
    if (spec.startsWith('.')) {
      return `from '${pathToFileURL(join(dirname(join(here, '..', relPath)), spec)).href}'`
    }
    return `from '${import.meta.resolve(spec)}'`
  })
  return import(`data:text/javascript;base64,${Buffer.from(rewritten, 'utf8').toString('base64')}`)
}

const { decideEntry, shouldSnapshot } = await loadTs('src/utils/stackOperationEntry.ts')

let passed = 0
const failures = []
function check(name, ok, detail) {
  if (ok) passed += 1
  else failures.push(detail === undefined ? name : `${name} —— ${detail}`)
}

/* ───────── 1. 首次挂载 show=true 且 autoStart → 必须自动开始 ───────── */

{
  const action = decideEntry({
    hasTask: false,
    taskFinished: false,
    startAttempted: false,
    autoStart: true,
    autoStarted: false
  })
  check('首挂载: autoStart 触发 startup', action === 'start-new', action)
  check('首挂载: 需要定格快照', shouldSnapshot(action) === true)
}

/* ───────── 2. autoStart 已触发过 → 重开不再自动开始 ───────── */

{
  const action = decideEntry({
    hasTask: false,
    taskFinished: false,
    startAttempted: false,
    autoStart: true,
    autoStarted: true
  })
  check(
    '重开: autoStart 不重复触发',
    action !== 'start-new',
    '重开会再发一次 POST 就是重复执行'
  )
  check('重开: 退化为等用户点', action === 'await-user', action)
  check('重开: 仍是全新任务，需定格', shouldSnapshot(action) === true)
}

/* ───────── 3. 已有未结束任务 → resume，绝不 start ───────── */

{
  const action = decideEntry({
    hasTask: true,
    taskFinished: false,
    startAttempted: true,
    autoStart: true,
    autoStarted: true
  })
  check('未结束: 只 resume', action === 'resume', action)
  check(
    '未结束: 不定格新快照',
    shouldSnapshot(action) === false,
    '改快照会让同一 request_id 带新 payload'
  )
}

/* ───────── 4. 已有终态任务 → 什么都不做 ───────── */

{
  const action = decideEntry({
    hasTask: true,
    taskFinished: true,
    startAttempted: true,
    autoStart: true,
    autoStarted: false
  })
  check('终态: 不自动重跑', action === 'do-nothing', action)
  check('终态: 不定格', shouldSnapshot(action) === false)
}

/* ───────── 5. 状态未知（POST 过但没任务）→ 同 key 重试，不换内容 ───────── */

{
  const action = decideEntry({
    hasTask: false,
    taskFinished: false,
    startAttempted: true,
    autoStart: true,
    autoStarted: true
  })
  check('未知: 同 key 重试', action === 'retry-same-key', action)
  check(
    '未知: 绝不定格新快照',
    shouldSnapshot(action) === false,
    '换 payload 会让 Hub 幂等校验返回 409'
  )
  check(
    '未知: 不自动 start',
    action !== 'start-new',
    '自动重发可能配上已被改动的内容'
  )
}

/* ───────── 6. 无 autoStart → 等用户点，但仍要定格 ───────── */

{
  const action = decideEntry({
    hasTask: false,
    taskFinished: false,
    startAttempted: false,
    autoStart: false,
    autoStarted: false
  })
  check('手动: 等用户点', action === 'await-user', action)
  check(
    '手动: 开窗即定格',
    shouldSnapshot(action) === true,
    '用户可能继续编辑，确认的内容必须固定'
  )
}

/* ───────── 8. 模拟完整开窗时序（用真实函数驱动，非正则） ───────── */

{
  /*
   * 模拟一个弹窗实例从创建到多次关开的过程。
   * 这里执行的是组件里同一份 decideEntry / shouldSnapshot —— 时序 bug
   * 正是在这些状态组合下出现，用状态机模拟比看源码可靠。
   */
  const sim = () => {
    // 实例内部状态
    const state = { hasTask: false, taskFinished: false, startAttempted: false, autoStarted: false }
    const log = { snapshots: 0, starts: 0, resumes: 0, posts: [] }

    /** 打开弹窗（对应 onShow 里的 switch） */
    function open(autoStart, contentAtOpen) {
      const action = decideEntry({ ...state, autoStart, autoStarted: state.autoStarted })
      if (shouldSnapshot(action)) {
        log.snapshots += 1
        log.lastSnapshotContent = contentAtOpen
      }
      switch (action) {
        case 'resume':
          log.resumes += 1
          break
        case 'start-new':
          state.autoStarted = true
          state.startAttempted = true
          log.starts += 1
          log.posts.push(log.lastSnapshotContent)
          break
        default:
          break
      }
      return action
    }

    return { state, log, open }
  }

  // 场景 A：首挂载 show=true + autoStart → 恰好一次 POST
  {
    const m = sim()
    m.open(true, 'v1')
    check('时序A: 首挂载 autoStart 发一次 start', m.log.starts === 1, String(m.log.starts))
    check('时序A: 快照被定格', m.log.snapshots === 1, String(m.log.snapshots))

    // 关窗再重开（父组件内容已被用户改成 v2）
    const a2 = m.open(true, 'v2')
    check(
      '时序A: 重开不二次 start',
      m.log.starts === 1,
      `重开后 starts=${String(m.log.starts)}，等于重复执行`
    )
    check('时序A: 重开仍不自动 POST', a2 !== 'start-new', a2)
    check('时序A: 只发过一次 POST 内容', m.log.posts.length === 1 && m.log.posts[0] === 'v1')
  }

  // 场景 B：状态未知时重开 → 同 key，且不改快照内容
  {
    const m = sim()
    m.open(true, 'v1')
    // 模拟 POST 抛出但 key 已用掉（startAttempted=true, hasTask=false）
    m.state.hasTask = false
    const a2 = m.open(true, 'v2-modified')
    check('时序B: 未知态判定同 key 重试', a2 === 'retry-same-key', a2)
    check(
      '时序B: 不重新定格快照',
      m.log.snapshots === 1,
      '改快照会让 Hub 幂等校验 409'
    )
    check('时序B: 不自动 POST', m.log.starts === 1, String(m.log.starts))
  }

  // 场景 C：任务进行中重开 → 只 resume
  {
    const m = sim()
    m.open(true, 'v1')
    m.state.hasTask = true
    m.state.taskFinished = false
    m.log.starts = 0
    m.log.resumes = 0
    const a2 = m.open(true, 'v2')
    check('时序C: 进行中只 resume', a2 === 'resume', a2)
    check('时序C: resume 一次', m.log.resumes === 1)
    check('时序C: 不 start', m.log.starts === 0)
    check('时序C: 没重新定格', m.log.snapshots === 1)
  }

  // 场景 D：任务已结束重开 → 什么都不做，等用户明确重跑
  {
    const m = sim()
    m.open(true, 'v1')
    m.state.hasTask = true
    m.state.taskFinished = true
    m.log.starts = 0
    const a2 = m.open(true, 'v2')
    check('时序D: 终态重开不动作', a2 === 'do-nothing', a2)
    check('时序D: 不 start', m.log.starts === 0)
  }

  // 场景 E：没有 autoStart 时首挂载不自动执行
  {
    const m = sim()
    const a = m.open(false, 'v1')
    check('时序E: 无 autoStart 不自动执行', m.log.starts === 0, String(m.log.starts))
    check('时序E: 等用户点', a === 'await-user', a)
    check(
      '时序E: 但仍定格快照',
      m.log.snapshots === 1,
      '用户继续编辑时，等待期间的内容必须冻结'
    )
  }

  // 场景 F：连点「开始」不会产生第二个 POST（start 幂等）
  {
    const m = sim()
    m.open(false, 'v1')
    // 用户点开始：此时 startAttempted 会被置位
    m.state.startAttempted = true
    m.state.autoStarted = true
    m.open(false, 'v1')
    const a = m.open(false, 'v1')
    check('时序F: 已发起过则不再 start', a === 'retry-same-key' || a === 'do-nothing', a)
  }
}

/* ───────── 9. 组件真的用了这些函数 + immediate ───────── */

{
  const modal = readFileSync(
    join(here, '..', 'src', 'components', 'stack', 'StackOperationModal.vue'),
    'utf8'
  )

  check('组件: 引入决策函数', /import \{[^}]*decideEntry[^}]*\} from '@\/utils\/stackOperationEntry'/.test(modal))
  check('组件: 调用 decideEntry', /decideEntry\(\{/.test(modal))
  check('组件: 调用 shouldSnapshot 守快照', /if \(shouldSnapshot\(action\)\) \{/.test(modal))

  /*
   * immediate 是本 bug 的核心修复点。
   * 非 immediate 时，父级常驻实例 + show 初始 true 的情况下永远不触发。
   */
  check(
    '组件: show watcher 是 immediate',
    /watch\(\s*\(\) => props\.show,\s*onShow,\s*\{ immediate: true \}\s*\)/.test(modal),
    '非 immediate 会导致首次 show=true 时 autoStart 不执行'
  )
  check(
    '组件: 提取了 onShow 供 watcher 与 mount 共用',
    /function onShow\(\): void \{/.test(modal)
  )

  // 快照只能在 shouldSnapshot 分支内，不能无条件 takeSnapshot
  const takeCalls = modal.match(/takeSnapshot\(\)/g) ?? []
  check(
    '组件: takeSnapshot 调用次数受控',
    takeCalls.length <= 3,
    `实际 ${String(takeCalls.length)} 处，超过说明有路径在无条件覆盖快照`
  )
  check(
    '组件: 重开路径不无条件定格',
    !/watch\([\s\S]{0,120}?=> props\.show,[\s\S]{0,200}?takeSnapshot\(\)/.test(modal) ||
      /if \(shouldSnapshot\(action\)\)[\s\S]{0,80}?takeSnapshot\(\)/.test(modal),
    '快照必须被 shouldSnapshot 保护'
  )
}

/* ───────── 8. finished 在失败终态也 emit（父级要拿 stack_version） ───────── */

{
  const modal = readFileSync(
    join(here, '..', 'src', 'components', 'stack', 'StackOperationModal.vue'),
    'utf8'
  )
  check(
    '终态通知: 成功与失败都 emit finished',
    /next !== 'done' && next !== 'failed'/.test(modal),
    '只在 done emit 会让保存失败时拿不到 stack_version'
  )
  check(
    '终态通知: removed 只在 done 且 record_removed',
    /next === 'done' && props\.action === 'remove' && isConfirmedRemove/.test(modal),
    '失败绝不能触发跳列表'
  )
}

/* ───────── 10. 第二次保存：新 version + 新 payload + 恰好一次新 POST ───────── */

{
  const { decideDeployRequest } = await loadTs('src/utils/stackDeployRequest.ts')

  /*
   * 模拟连续两次保存。这里执行的是组件里同一份决策函数，
   * 关注的是"第二次点保存，到底会不会真的跑新内容"——
   * 这正是常驻弹窗最容易出错的地方：只改 props 不会换 runner，
   * 用户看到旧日志还以为新内容已部署。
   */
  const mkState = (over) => ({
    dirty: false,
    inFlight: false,
    settled: false,
    attempted: false,
    ...over
  })

  // 第一次保存：从没跑过 + 有改动 → 新一轮
  check(
    '保存1: 全新一轮',
    decideDeployRequest(mkState({ dirty: true })) === 'new-run'
  )

  // 任务在跑时再点保存 → 只看，不重复部署
  check(
    '保存中: 不重复部署',
    decideDeployRequest(mkState({ dirty: true, inFlight: true, attempted: true })) === 'view',
    '在跑时开新任务就是重复下发'
  )

  // 任务已确认终态 + 用户改了内容 → 必须真的起新一轮
  check(
    '终态后再保存: 起新一轮',
    decideDeployRequest(mkState({ dirty: true, settled: true, attempted: true })) === 'new-run',
    '否则用户看到旧日志会以为新内容已部署'
  )

  // 发起过但没拿到终态（503 / 关窗打断）→ 不能换 key
  check(
    '状态未明: 不换 key',
    decideDeployRequest(mkState({ dirty: true, attempted: true, settled: false })) === 'view',
    '服务器上可能已有任务，换 request_id 会重复部署'
  )

  // 没改内容只是要再看窗口 → 什么都不发
  check(
    '无改动: 只查看',
    decideDeployRequest(mkState({ dirty: false, settled: true, attempted: true })) === 'view'
  )

  /* 端到端模拟：两次成功保存 → 两个不同 request_id、两次 payload 各不相同 */
  {
    const posts = []
    let generation = 0
    const state = { dirty: false, inFlight: false, settled: false, attempted: false }
    const reqIds = []

    function clickSave(content, version) {
      state.dirty = true
      const decision = decideDeployRequest(state)
      if (decision === 'view') return decision
      generation += 1
      state.settled = false
      state.attempted = true
      state.inFlight = true
      // 模拟：每轮自己去申请一个 request_id（真实实现由 runner 生成）
      const requestId = `req-${String(generation)}`
      reqIds.push(requestId)
      posts.push({ requestId, content, version })
      return decision
    }

    /** 模拟任务终态 */
    function settle() {
      state.inFlight = false
      state.settled = true
      state.dirty = false
    }

    clickSave('v1-content', 2)
    settle()
    clickSave('v2-content', 3)

    check('连存: 产生两次 POST', posts.length === 2, String(posts.length))
    check(
      '连存: 新的 payload 是新内容',
      posts[1].content === 'v2-content',
      '旧内容说明第二次保存没有生效'
    )
    check('连存: 新 version', posts[1].version === 3, String(posts[1].version))
    check(
      '连存: 两个不同 request_id',
      new Set(reqIds).size === 2,
      '同一 id 表示第二次只是重试旧任务，不是新部署'
    )
    check('连存: generation 前进两次', generation === 2, String(generation))
  }

  /* 端到端：进行中再点保存 → 不产生第二个 POST */
  {
    const posts = []
    let generation = 0
    const state = { dirty: false, inFlight: false, settled: false, attempted: false }

    state.dirty = true
    decideDeployRequest(state)
    generation += 1
    state.attempted = true
    state.inFlight = true
    posts.push('req-1')

    // 用户在任务还在跑时又改了内容并点保存
    state.dirty = true
    const d2 = decideDeployRequest(state)

    check('进行中再存: 只查看', d2 === 'view', d2)
    check('进行中再存: 没有第二个 POST', posts.length === 1, String(posts.length))
    check('进行中再存: generation 不变', generation === 1, String(generation))
  }
}

/* ───────── 11. 组件接线：generation 进 key，settled 来自终态事件 ───────── */

{
  const panel = readFileSync(
    join(here, '..', 'src', 'components', 'stack', 'ManagedStackConfigPanel.vue'),
    'utf8'
  )
  const cfg = readFileSync(
    join(here, '..', 'src', 'composables', 'useManagedStackConfig.ts'),
    'utf8'
  )

  check(
    '接线: 弹窗 key 含 generation',
    /:key="[\s\S]{0,80}?config\.deployGeneration\.value/.test(panel),
    '不换 key 就不会有新 runner，第二次保存会显示旧日志'
  )
  check(
    '接线: 弹窗常驻不 v-if',
    !/<stack-operation-modal[^>]*\bv-if=/.test(panel),
    'v-if 会在关闭时卸载，任务与 request_id 一起丢'
  )
  check(
    '接线: 保存走 requestDeploy',
    /@click="openDeployModal"/.test(panel) && /function openDeployModal/.test(panel)
  )
  check(
    '接线: 新一轮才升 generation',
    /deployGeneration\.value \+= 1/.test(cfg)
  )
  check(
    '接线: 终态事件才置 settled',
    /deploySettled\.value = true/.test(cfg)
  )
  /*
   * 决策放在组件层而不是 composable：composable 被共享契约测试直接 import，
   * 那个测试把所有 @/ 依赖替换成同一个 stub，新增依赖会把它打挂。
   */
  check(
    '接线: 决策用纯函数',
    /decideDeployRequest\(\{/.test(panel)
  )
  check(
    '接线: composable 不新增 @/ 依赖',
    !/from '@\/utils\/stackDeployRequest'/.test(cfg),
    '共享测试的 stub loader 会把所有 @/ 依赖替换成同一个桩'
  )
}

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 弹窗入口逻辑测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 弹窗入口逻辑测试：${passed} 通过，0 失败`)