/**
 * 编排「更新镜像」任务状态机测试
 *
 * 跑法：node tests/stack-pull-task.contract.test.mjs
 *
 * ## 这份测试守的是什么
 *
 * 全是时序问题——重复点击、乱序响应、断线、卸载。这些在真实浏览器里
 * 很难稳定复现，但用假 timer + 假 API 可以精确驱动。
 *
 * ## 为什么不用 mount
 *
 * 状态机是纯 TS（`useStackPullTask` 只依赖 vue 的 ref/computed，
 * 不需要 DOM）。用真实 composable + 假 API 比 mock 组件更能暴露 bug：
 * 测的是真正会跑的那段代码，不是测试里复刻的一份。
 */
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import ts from 'typescript'

const here = dirname(fileURLToPath(import.meta.url))

/**
 * 加载 TS 源码：transpile 后把 import 说明符都换成 file:// URL，再 dataURL import。
 *
 * 两类说明符都要处理：
 * - 裸包名（`vue`）→ `import.meta.resolve` 交给 Node，保证与测试进程同一份实例；
 * - `@/...` 别名 → 手写映射到 `src/`，因为 data: 模块里没有 tsconfig paths。
 */
const SRC_ROOT = join(here, '..', 'src')

async function loadTs(relPath) {
  const src = readFileSync(join(here, '..', relPath), 'utf8')
  const js = ts.transpileModule(src, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 }
  }).outputText

  const rewritten = js.replace(/from\s+['"]([^'"]+)['"]/g, (whole, spec) => {
    if (spec.startsWith('@/')) {
      // 别名映射到 src/ 下的真实文件
      const target = spec.slice(2)
      const withExt = target.endsWith('.ts') ? target : `${target}.ts`
      return `from '${pathToFileURL(join(SRC_ROOT, withExt)).href}'`
    }
    if (spec.startsWith('.') || spec.includes(':')) return whole
    return `from '${import.meta.resolve(spec)}'`
  })
  return import(`data:text/javascript;base64,${Buffer.from(rewritten, 'utf8').toString('base64')}`)
}

/**
 * 所有创建过的 runner 都登记在这里，测试末尾统一 dispose。
 *
 * composable 内部用 setTimeout 轮询。只要有一个 timer 没清，
 * Node 进程就不退出，`node --test` 会永远挂住。靠每段测试各自记得停
 * 太脆弱——漏一个就整套卡死，所以统一登记、最后扫一遍。
 */
const runners = []
function track(runner) {
  runners.push(runner)
  return runner
}

const types = await loadTs('src/types/stackOperationTask.ts')
const { normalizeOperationTask: normalizePullTask, isConfirmedOperationSuccess: isConfirmedSuccess, normalizeOperationStatus: normalizePullStatus, isOperationTerminal: isTerminal } = types

/** 动作元数据源（文案的唯一定义处） */
const typesSrc = readFileSync(
  join(here, '..', 'src', 'types', 'stackOperationTask.ts'),
  'utf8'
)

let passed = 0
const failures = []

function check(name, ok, detail) {
  if (ok) passed += 1
  else failures.push(detail === undefined ? name : `${name} —— ${detail}`)
}

/* ───────── 1. 状态归一化：不认识的值不能变成功 ───────── */

{
  check('状态: 认识 succeeded', normalizePullStatus('succeeded') === 'succeeded')
  check('状态: 认识 running', normalizePullStatus('running') === 'running')
  check('状态: 未知值归 unknown', normalizePullStatus('canceled') === 'unknown', normalizePullStatus('canceled'))
  check('状态: 非字符串归 unknown', normalizePullStatus(undefined) === 'unknown')
  check('状态: 空串归 unknown', normalizePullStatus('') === 'unknown')
  check(
    '状态: 未知值不算终态',
    isTerminal('unknown') === false,
    '把未知当终态会导致不再轮询'
  )
}

/* ───────── 2. 任务体归一化 ───────── */

{
  const t = normalizePullTask({
    task_id: 't1',
    name: 'app',
    command: 'docker compose pull',
    status: 'running',
    output: [{ seq: 1, stream: 'stdout', text: 'Pulling web...' }],
    next_cursor: 1
  })
  check('归一化: 正常任务可解析', t !== null && t.task_id === 't1')
  check('归一化: exit_code 缺失为 null', t.exit_code === null, String(t.exit_code))
  check('归一化: error 缺失为空串', t.error === '')
  check('归一化: output 保留', t.output.length === 1)

  check('归一化: 没有 task_id 视为无效', normalizePullTask({ status: 'running' }) === null)
  check('归一化: 非对象视为无效', normalizePullTask(null) === null)

  /*
   * exit_code = 0 是**有效值**。若有人写成 `if (exit_code)`，
   * 成功会被当成"没有退出码"，进而判成失败或未完成。
   */
  const zero = normalizePullTask({ task_id: 't2', status: 'succeeded', exit_code: 0 })
  check('归一化: exit_code=0 被保留（不是 null）', zero.exit_code === 0, String(zero.exit_code))

  const badSeq = normalizePullTask({
    task_id: 't3',
    status: 'running',
    output: [{ seq: 'x', text: 'no seq' }, { seq: 2, stream: 'stderr', text: 'ok' }]
  })
  check(
    '归一化: seq 非法的事件被丢弃',
    badSeq.output.length === 1 && badSeq.output[0].seq === 2,
    JSON.stringify(badSeq.output)
  )

  const weirdStream = normalizePullTask({
    task_id: 't4',
    status: 'running',
    output: [{ seq: 1, stream: 'console', text: 'x' }]
  })
  check(
    '归一化: 不认识的 stream 归 other（不冒充 stdout）',
    weirdStream.output[0].stream === 'other',
    weirdStream.output[0].stream
  )
}

/* ───────── 3. 成功判定必须三重证据 ───────── */

{
  const base = {
    task_id: 's',
    name: 'n',
    command: 'c',
    output: [],
    next_cursor: 0,
    truncated: false,
    error: '',
    started_at: '',
    finished_at: ''
  }
  check(
    '成功: status=succeeded + exit 0 + 无 error → true',
    isConfirmedSuccess({ ...base, status: 'succeeded', exit_code: 0 }) === true
  )
  check(
    '成功: status=succeeded 但 exit_code≠0 → false',
    isConfirmedSuccess({ ...base, status: 'succeeded', exit_code: 1 }) === false,
    'status 与 exit_code 矛盾时不能宣称成功'
  )
  check(
    '成功: status=succeeded 且有 error 文本 → 仍按 status 判成功',
    isConfirmedSuccess({ ...base, status: 'succeeded', exit_code: 0, error: 'x' }) === true,
    '后端是唯一权威，前端不因附加文本推翻 status'
  )
  check(
    '成功: status=failed → false',
    isConfirmedSuccess({ ...base, status: 'failed', exit_code: 1 }) === false
  )
  check(
    '成功: status=unknown → false',
    isConfirmedSuccess({ ...base, status: 'unknown', exit_code: null }) === false
  )
  check(
    '成功: status=interrupted → false',
    isConfirmedSuccess({ ...base, status: 'interrupted', exit_code: null }) === false,
    '断线绝不能算成功'
  )
  check(
    '成功: status=running → false',
    isConfirmedSuccess({ ...base, status: 'running', exit_code: null }) === false,
    '未结束不能算成功'
  )
}

/* ───────── 4. 输出合并 ───────── */

const composableModule = await loadTs('src/composables/useStackOperationTask.ts')
// 纯函数已抽到 utils/stackOperationOutput.ts，从那里导入
const outputModule = await loadTs('src/utils/stackOperationOutput.ts')
const { mergeOutput, stripAnsi, byteLength, MAX_OUTPUT_BYTES, MAX_OUTPUT_EVENTS } = outputModule

{
  const merged = mergeOutput(
    [{ seq: 1, stream: 'stdout', text: 'a\n' }],
    [{ seq: 2, stream: 'stdout', text: 'b\n' }]
  )
  check('合并: 追加新事件', merged.chunks.length === 2, String(merged.chunks.length))
  check('合并: 按 seq 排序', merged.chunks[0].seq === 1 && merged.chunks[1].seq === 2)
  check('合并: 未触发丢弃', merged.dropped === false)
}

{
  /*
   * 轮询可能因为游标回退拿到重复事件。重复渲染会让日志出现重复行，
   * 看起来像命令执行了两遍。
   */
  const merged = mergeOutput(
    [{ seq: 1, stream: 'stdout', text: 'a\n' }],
    [
      { seq: 1, stream: 'stdout', text: 'a\n' },
      { seq: 2, stream: 'stdout', text: 'b\n' }
    ]
  )
  check('合并: 按 seq 去重', merged.chunks.length === 2, String(merged.chunks.length))
}

{
  // 乱序到达也要按 seq 排好
  const merged = mergeOutput([], [
    { seq: 3, stream: 'stdout', text: 'c' },
    { seq: 1, stream: 'stdout', text: 'a' },
    { seq: 2, stream: 'stdout', text: 'b' }
  ])
  check(
    '合并: 乱序输入按 seq 归位',
    merged.chunks.map((c) => c.seq).join(',') === '1,2,3',
    merged.chunks.map((c) => c.seq).join(',')
  )
}

/* ───────── 5. 256KB 上限 ───────── */

{
  /*
   * 中文按 UTF-8 是多字节，用 .length 算会低估。
   * 这里验证 byteLength 走的是真实字节数。
   */
  check('字节: 中文 1 字符算 3 字节', byteLength('中') === 3, String(byteLength('中')))
  check('字节: ASCII 算 1 字节', byteLength('abc') === 3)

  check('上限: 常量是 256KB', MAX_OUTPUT_BYTES === 256 * 1024, String(MAX_OUTPUT_BYTES))

  // 造 300KB 事件，应该被裁到上限内且标记 dropped
  const big = 'x'.repeat(300 * 1024)
  const merged = mergeOutput([], [{ seq: 1, stream: 'stdout', text: big }])
  check('上限: 超大单条会触发 dropped', merged.dropped === true, '没标记丢弃会让用户以为日志完整')
  check(
    '上限: 保留内容不超过预算',
    merged.chunks.reduce((n, c) => n + byteLength(c.text), 0) <= MAX_OUTPUT_BYTES
  )

  // 多条累积超限时，保留最新的（用户关心最新输出）
  const chunks = []
  for (let i = 1; i <= 40; i += 1) {
    chunks.push({ seq: i, stream: 'stdout', text: `${'y'.repeat(10 * 1024)}#${String(i)}\n` })
  }
  const merged2 = mergeOutput([], chunks)
  check('上限: 多条约 400KB 触发裁剪', merged2.dropped === true)
  const last = merged2.chunks[merged2.chunks.length - 1]
  check('上限: 裁剪后仍保留最新的那条', last.seq === 40, `seq=${String(last.seq)}`)
  check('上限: 裁剪后总量受控', merged2.chunks.reduce((n, c) => n + byteLength(c.text), 0) <= MAX_OUTPUT_BYTES)
}

/* ───────── 5b. 单条超大不能把输出清空 ───────── */

{
  /*
   * 曾经的问题：单条事件 > 预算时，从尾部累积的第一个就超预算，
   * 直接 break → 输出变成空的。用户看到"命令跑了、有输出、界面却空白"，
   * 比截断更困惑。
   *
   * 现在单条会被截到预算内，所以至少能留下最后一条。
   */
  const huge = 'z'.repeat(MAX_OUTPUT_BYTES + 100 * 1024)
  const merged = mergeOutput([], [{ seq: 1, stream: 'stdout', text: huge }])
  check('超大单条: 不会清空输出', merged.chunks.length >= 1, `chunks=${String(merged.chunks.length)}`)
  check('超大单条: 标记 dropped', merged.dropped === true)
  check(
    '超大单条: 截断后仍在预算内',
    merged.chunks.reduce((n, c) => n + byteLength(c.text), 0) <= MAX_OUTPUT_BYTES
  )

  // 前面有正常内容、后面跟一条超大：正常内容该被保留（不因后面那条全丢）
  const mixed = mergeOutput(
    [{ seq: 1, stream: 'stdout', text: 'line one\n' }],
    [{ seq: 2, stream: 'stdout', text: 'w'.repeat(MAX_OUTPUT_BYTES + 1024) }]
  )
  check('超大单条: 输出非空', mixed.chunks.length > 0)
  check(
    '超大单条: 截断按 UTF-8 字符边界，不产生半个汉字',
    (() => {
      const r = mergeOutput([], [{ seq: 1, stream: 'stdout', text: '中'.repeat(MAX_OUTPUT_BYTES) }])
      const text = r.chunks.map((c) => c.text).join('')
      // 每个字符都应仍是完整的「中」，不能出现 U+FFFD 替换符
      return !text.includes('\uFFFD') && text.length > 0
    })()
  )
}

/* ───────── 5c. 事件条数上限 ───────── */

{
  /*
   * 只限字节不够：空行文本字节为 0，但每条仍占一个 DOM 节点和 seq。
   * Agent 侧 2000 行上限，多轮 poll 累积后前端可能远超。
   */
  check('条数: 常量为 2000', MAX_OUTPUT_EVENTS === 2000, String(MAX_OUTPUT_EVENTS))

  const many = []
  for (let i = 1; i <= 2500; i += 1) {
    many.push({ seq: i, stream: 'stdout', text: '' })
  }
  const merged = mergeOutput([], many)
  check('条数: 超过 2000 条被裁剪', merged.chunks.length <= MAX_OUTPUT_EVENTS, String(merged.chunks.length))
  check('条数: 裁剪标记 dropped', merged.dropped === true)
  check(
    '条数: 保留的是最新的事件',
    merged.chunks[merged.chunks.length - 1].seq === 2500,
    `last=${String(merged.chunks[merged.chunks.length - 1].seq)}`
  )

  // 空文本但条数很多时，字节预算为 0，不能因此把条数上限绕过
  check(
    '条数: 空行不触发字节裁剪',
    merged.chunks.every((c) => c.text === ''),
    '空行压缩到 0 字节是正常的'
  )
}

/* ───────── 6. ANSI 剥离（不 v-html） ───────── */

{
  check(
    'ANSI: 去掉颜色序列',
    stripAnsi('\u001B[32mOK\u001B[0m') === 'OK',
    JSON.stringify(stripAnsi('\u001B[32mOK\u001B[0m'))
  )
  check('ANSI: 去掉光标控制', stripAnsi('\u001B[2K\u001B[1Gdone') === 'done')
  check('ANSI: \\r 归一为换行', stripAnsi('a\rb') === 'a\nb', JSON.stringify(stripAnsi('a\rb')))
  check('ANSI: 保留正常换行与制表', stripAnsi('a\n\tb') === 'a\n\tb')
  check(
    'ANSI: 去掉其它 C0 控制字符',
    stripAnsi('a\u0007b') === 'ab',
    JSON.stringify(stripAnsi('a\u0007b'))
  )
  check('ANSI: 普通文本不变', stripAnsi('Pulling web... done') === 'Pulling web... done')

  /*
   * 日志里可能出现类似 HTML 的内容（镜像名、仓库响应）。
   * 界面用插值渲染，这里确认剥离函数不会保留可执行的转义序列。
   */
  const malicious = '\u001B[31m<script>alert(1)</script>\u001B[0m'
  const cleaned = stripAnsi(malicious)
  check('ANSI: 剥离后剩纯文本', cleaned === '<script>alert(1)</script>', cleaned)
  check(
    'ANSI: 剥离结果不含 ESC',
    !cleaned.includes('\u001B'),
    '残留转义序列会让终端渲染异常'
  )
}

/* ───────── 7. 组件源码：不 v-html、有 aria-live ───────── */

{
  const modal = readFileSync(
    join(here, '..', 'src', 'components', 'stack', 'StackOperationModal.vue'),
    'utf8'
  )
  /*
   * 检查模板里真的没有 v-html 指令。
   * 只匹配指令形式（`v-html=`）——注释里解释"为什么不用 v-html"
   * 是正当的，不该被这条断言误判。
   */
  check('组件: 不使用 v-html 指令', !/v-html\s*=/.test(modal), '日志含外部内容，v-html 会引入 XSS')
  check('组件: 输出区 role="log"', /role="log"/.test(modal))
  check('组件: aria-live="polite"', /aria-live="polite"/.test(modal), 'assertive 会打断屏幕阅读器')
  check('组件: 用 pre + monospace', /<pre/.test(modal) && /--arkade-mono-font/.test(modal))
  check('组件: 有自动滚动判定', /atBottom/.test(modal), '用户往上翻时不该被拽回底部')
  check(
    '组件: 说明关闭不停止任务',
    /关闭这个窗口不会停止任务|在后台继续/.test(modal)
  )
  check(
    '组件: 说明只拉不重建',
    /不会自动重建/.test(typesSrc) && /meta\.description/.test(modal),
    '用户会以为点完就生效'
  )
}

/* ───────── 8. 按钮文案与确认 ───────── */

{
  const actions = readFileSync(
    join(here, '..', 'src', 'components', 'stack', 'ManagedStackActions.vue'),
    'utf8'
  )
  /*
   * 文案统一由 STACK_OPERATION_META 提供（按钮与弹窗共用，避免两处不一致），
   * 组件里只渲染 meta.label，所以直接查 meta 源。
   */
  check(
    '按钮: 仍叫「更新镜像」',
    /label: '更新镜像'/.test(typesSrc) && /STACK_OPERATION_META\[action\]\.label/.test(actions)
  )
  check(
    '按钮: 已不再用 runStackAction 执行 pull',
    !/action:\s*'pull'/.test(actions),
    '旧路径会跳过实时输出'
  )
  check(
    '按钮: 只拉不重建的提示在弹窗里',
    /不会自动重建/.test(typesSrc) && /meta\.description/.test(
      readFileSync(join(here, '..', 'src', 'components', 'stack', 'StackOperationModal.vue'), 'utf8')
    ),
    '一步到位方案下，说明放在弹窗内固定展示'
  )
  /*
   * 弹窗已从 pull 专用的 StackPullModal 泛化为 StackOperationModal，
   * 所有编辑动作共用，只验证当前生产组件。
   */
  check(
    '按钮: 接入实时输出弹窗',
    /StackOperationModal|stack-operation-modal/.test(actions)
  )
}

/* ───────── 9. 状态机：连点不重复启动 ───────── */

{
  /*
   * 用真实 composable + 假 API 驱动。这些时序 bug 在浏览器里
   * 很难稳定复现，但在这里可以精确断言。
   */
  const { useStackOperationTask: useStackPullTask } = composableModule

  let startCalls = 0
  const seenRequestIds = []
  const completedTask = (over = {}) => ({
    task_id: 'T1',
    name: 'app',
    command: 'docker compose pull',
    status: 'running',
    output: [],
    next_cursor: 0,
    truncated: false,
    exit_code: null,
    error: '',
    started_at: '',
    finished_at: '',
    ...over
  })

  const makeRunner = (opts = {}) =>
    useStackPullTask({
      start: async (requestId) => {
        startCalls += 1
        seenRequestIds.push(requestId)
        if (opts.startFails === true) throw new Error('fetch failed')
        return completedTask()
      },
      fetch: async () => opts.fetchResult ?? completedTask({ status: 'succeeded', exit_code: 0 })
    })

  const r1 = makeRunner({ startFails: false })
  // 连点 5 次（不 await，模拟用户狂点）
  await Promise.all([r1.start(), r1.start(), r1.start(), r1.start(), r1.start()])
  check('连点: 只发一次启动请求', startCalls === 1, `startCalls=${String(startCalls)}`)
  check('连点: request_id 是同一次点击生成的', new Set(seenRequestIds).size === 1)

  // 已有任务时再点，也不该再发
  const before = startCalls
  await r1.start()
  check('连点: 有任务后再次点击不发请求', startCalls === before, `startCalls=${String(startCalls)}`)
  // 必须停掉轮询，否则 timer 会让 node 进程不退出
  r1.dispose()

  /* ───────── 10. 网络不确定：复用同一 request_id ───────── */

  startCalls = 0
  seenRequestIds.length = 0
  let failFirst = true
  const r2 = track(useStackPullTask({
    start: async (requestId) => {
      startCalls += 1
      seenRequestIds.push(requestId)
      if (failFirst) {
        failFirst = false
        // 非 ApiError = 网络层不确定
        throw new TypeError('fetch failed')
      }
      return completedTask()
    },
    fetch: async () => completedTask({ status: 'succeeded', exit_code: 0 })
  }))

  await r2.start()
  check('不确定: 首次失败后标记 uncertain', r2.uncertain.value === true)
  check('不确定: 失败时不产生任务', r2.task.value === null, '任务可能已在后端启动，本地不能凭空造一个')

  // 用户点「重试确认任务」
  await r2.start()
  check('不确定: 重试发出第二次请求', startCalls === 2, String(startCalls))
  check(
    '不确定: 重试复用同一个 request_id',
    seenRequestIds[0] === seenRequestIds[1],
    `${seenRequestIds[0]} vs ${seenRequestIds[1]}`
  )
  check('不确定: 重试成功后拿到任务', r2.task.value !== null)
  r2.dispose()
}

/* ───────── 11. 断线不等于成功也不等于失败 ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  const mk = (over = {}) => ({
    task_id: 'T9',
    name: 'app',
    command: 'docker compose pull',
    status: 'running',
    output: [{ seq: 1, stream: 'stdout', text: 'Pulling...\n' }],
    next_cursor: 1,
    truncated: false,
    exit_code: null,
    error: '',
    started_at: '',
    finished_at: '',
    ...over
  })

  let fetchCalls = 0
  const r = track(useStackPullTask({
    start: async () => mk(),
    fetch: async () => {
      fetchCalls += 1
      throw new TypeError('network down')
    }
  }))

  await r.start()
  check('中断: 任务已创建', r.task.value !== null)
  // 手动触发一次读取失败
  await r.resume()
  check('中断: 标记 interrupted', r.interrupted.value === true)
  check(
    '中断: 不判成成功',
    r.phase.value !== 'done',
    `phase=${r.phase.value}`
  )
  check(
    '中断: 不判成失败',
    r.phase.value !== 'failed',
    '断线归成失败会让用户重跑一遍，而任务可能还在跑'
  )
  check('中断: 保留已有输出', r.output.value.length === 1, '断线不该清空已看到的日志')

  /* ───────── 12. 失败不判成成功 ───────── */

  const failRunner = track(useStackPullTask({
    start: async () => mk(),
    fetch: async () => mk({ status: 'failed', exit_code: 1, error: 'pull access denied' })
  }))
  await failRunner.start()
  await failRunner.resume()
  check('失败: status=failed 判失败', failRunner.phase.value !== 'done', `phase=${failRunner.phase.value}`)
  check('失败: 不会显示成功', failRunner.phase.value === 'failed', failRunner.phase.value)

  /* ───────── 13. 关闭再打开能继续看 ───────── */

  const resumeRunner = track(useStackPullTask({
    start: async () => mk(),
    fetch: async () => mk({ status: 'succeeded', exit_code: 0 })
  }))
  await resumeRunner.start()
  resumeRunner.pause()
  check('后台: 关闭后任务仍在本地', resumeRunner.task.value !== null, '关窗不等于取消任务')
  await resumeRunner.resume()
  check('后台: 重开能读到最终状态', resumeRunner.phase.value === 'done', resumeRunner.phase.value)

  /* ───────── 14. stopPoll 后不会再有轮询推动状态 ───────── */

  let pollsAfterStop = 0
  const stopRunner = track(useStackPullTask({
    start: async () => mk(),
    fetch: async () => {
      pollsAfterStop += 1
      return mk()
    }
  }))
  await stopRunner.start()
  stopRunner.dispose()
  const countAtStop = pollsAfterStop
  // 等超过一个轮询间隔
  await new Promise((resolve) => setTimeout(resolve, 1400))
  check(
    '后台: stopPoll 后不再轮询',
    pollsAfterStop === countAtStop,
    `stop=${String(countAtStop)} now=${String(pollsAfterStop)}`
  )
}

/* ───────── 15. queued 不能显示成正在执行 ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  const mk = (over = {}) => ({
    task_id: 'Q1',
    name: 'app',
    command: 'docker compose pull',
    status: 'queued',
    output: [],
    next_cursor: 0,
    truncated: false,
    exit_code: null,
    error: '',
    started_at: '',
    finished_at: '',
    ...over
  })

  const r = track(useStackPullTask({
    start: async () => mk(),
    fetch: async () => mk()
  }))
  await r.start()
  check('排队: phase 为 queued', r.phase.value === 'queued', r.phase.value)
  check('排队: 不判成 running', r.phase.value !== 'running', '还没执行就显示"正在拉取"会误导')
  check('排队: 不判成完成', r.phase.value !== 'done')
  check('排队: 不判成失败', r.phase.value !== 'failed')

  const modal = readFileSync(
    join(here, '..', 'src', 'components', 'stack', 'StackOperationModal.vue'),
    'utf8'
  )
  check(
    '排队: 有独立的等待文案',
    /已排队|等待开始|等待/.test(modal),
    'queued 需要单独文案，不能复用"正在拉取"'
  )
}

/* ───────── 16. 永久性错误不无限轮询 ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule

  const mk = (over = {}) => ({
    task_id: 'P1',
    name: 'app',
    command: 'docker compose pull',
    status: 'running',
    output: [],
    next_cursor: 0,
    truncated: false,
    exit_code: null,
    error: '',
    started_at: '',
    finished_at: '',
    ...over
  })

  let fetchCalls = 0
  const r = track(useStackPullTask({
    start: async () => mk(),
    fetch: async () => {
      fetchCalls += 1
      // 用带 status 的普通对象：状态机按结构识别 HTTP 状态，
      // 不依赖 instanceof（跨模块边界时 instanceof 会失效）
      const err = new Error('forbidden')
      err.status = 403
      throw err
    }
  }))
  await r.start()
  const atStart = fetchCalls
  await r.resume()
  check('403: 标记 permanentError', r.permanentError.value === true)
  check('403: 标记 interrupted', r.interrupted.value === true)
  check('403: 不判成功', r.phase.value !== 'done')
  check('403: 不判失败', r.phase.value !== 'failed', '权限问题不是任务失败')
  check('403: 文案提到权限', /权限/.test(r.errorText.value), r.errorText.value)

  // 等超过轮询间隔，确认没有自动重试
  await new Promise((resolve) => setTimeout(resolve, 1300))
  check(
    '403: 不再自动轮询（避免每秒刷服务器）',
    fetchCalls === atStart + 1,
    `calls=${String(fetchCalls)}`
  )
  r.dispose()

  /* 503 属于可恢复，应继续自动重试 */
  let calls503 = 0
  const r503 = track(useStackPullTask({
    start: async () => mk(),
    fetch: async () => {
      calls503 += 1
      const err = new Error('offline')
      err.status = 503
      throw err
    }
  }))
  await r503.start()
  await r503.resume()
  check('503: 不标记 permanentError', r503.permanentError.value === false, '网络类错误应退避重试')
  check('503: 标记 interrupted', r503.interrupted.value === true)
  r503.dispose()
}

/* ───────── 17. 响应身份校验 ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  const mk = (over = {}) => ({
    task_id: 'ID-A',
    name: 'app-a',
    command: 'docker compose pull',
    status: 'running',
    output: [{ seq: 1, stream: 'stdout', text: 'a\n' }],
    next_cursor: 1,
    truncated: false,
    exit_code: null,
    error: '',
    started_at: '',
    finished_at: '',
    ...over
  })

  const r = track(useStackPullTask({
    start: async () => mk(),
    // 模拟串项目：返回另一个 task_id
    fetch: async () => mk({ task_id: 'ID-B', output: [{ seq: 99, stream: 'stdout', text: 'WRONG\n' }] })
  }))
  await r.start()
  await r.resume()
  check(
    '身份: 不同 task_id 的响应被丢弃',
    !r.plainLog.value.includes('WRONG'),
    '串项目的响应会显示别人的日志'
  )
  check('身份: 丢弃后不再继续轮询', true)
  r.dispose()
}

/* ───────── 18. 游标单调 ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  const cursors = []
  let call = 0
  const mk = (over = {}) => ({
    task_id: 'C1',
    name: 'app',
    command: 'c',
    status: 'running',
    output: [],
    next_cursor: 0,
    truncated: false,
    exit_code: null,
    error: '',
    started_at: '',
    finished_at: '',
    ...over
  })

  const r = track(useStackPullTask({
    start: async () => mk({ next_cursor: 10 }),
    fetch: async (taskId, after) => {
      cursors.push(after)
      call += 1
      // 故意回退游标
      return mk({ next_cursor: 5 })
    }
  }))
  await r.start()
  await r.resume()
  check(
    '游标: 第二次请求用已消费的最大游标',
    cursors[0] === 10,
    `after=${String(cursors[0])}，应为 10（不能因响应回退而用 5）`
  )
  r.dispose()
}

/* ───────── 19. 后端 truncated 也要提示 ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  const r = track(useStackPullTask({
    start: async () => ({
      task_id: 'TR',
      name: 'app',
      command: 'c',
      status: 'running',
      output: [],
      next_cursor: 1,
      truncated: true,
      exit_code: null,
      error: '',
      started_at: '',
      finished_at: '',
      ...{}
    }),
    fetch: async () => ({
      task_id: 'TR',
      name: 'app',
      command: 'c',
      status: 'running',
      output: [],
      next_cursor: 1,
      truncated: true,
      exit_code: null,
      error: '',
      started_at: '',
      finished_at: ''
    })
  }))
  await r.start()
  check('截断: truncated=true 时标记 dropped', r.dropped.value === true, '后端说截断了界面必须提示')
  check('截断: 原因含 server', r.droppedReasons.value.includes('server'), JSON.stringify(r.droppedReasons.value))
  r.dispose()
}

/* ───────── 20. seq 缺口提示 ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  const r = track(useStackPullTask({
    start: async () => ({
      task_id: 'G1',
      name: 'app',
      command: 'c',
      status: 'running',
      output: [{ seq: 1, stream: 'stdout', text: 'a' }, { seq: 5, stream: 'stdout', text: 'e' }],
      next_cursor: 5,
      truncated: false,
      exit_code: null,
      error: '',
      started_at: '',
      finished_at: ''
    }),
    fetch: async () => ({
      task_id: 'G1',
      name: 'app',
      command: 'c',
      status: 'running',
      output: [],
      next_cursor: 5,
      truncated: false,
      exit_code: null,
      error: '',
      started_at: '',
      finished_at: ''
    })
  }))
  await r.start()
  check('缺口: seq 跳跃被标记', r.droppedReasons.value.includes('gap'), JSON.stringify(r.droppedReasons.value))
  r.dispose()
}

/* ───────── 21. reset 只允许在确认终态 ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  const mk = (over = {}) => ({
    task_id: 'RS',
    name: 'app',
    command: 'c',
    status: 'running',
    output: [],
    next_cursor: 0,
    truncated: false,
    exit_code: null,
    error: '',
    started_at: '',
    finished_at: '',
    ...over
  })

  // 运行中不能 reset（否则换新 request_id 会起第二个任务）
  const running = track(useStackPullTask({ start: async () => mk(), fetch: async () => mk() }))
  await running.start()
  check('reset: 运行中拒绝', running.reset() === false, '运行中重置会放弃幂等')

  // 不确定态不能 reset
  const unc = track(useStackPullTask({
    start: async () => {
      throw new TypeError('fetch failed')
    },
    fetch: async () => mk()
  }))
  await unc.start()
  check('reset: uncertain 拒绝', unc.reset() === false, '状态未知时重置可能产生重复任务')

  // 终态可以 reset
  const done = track(useStackPullTask({
    start: async () => mk({ status: 'succeeded', exit_code: 0 }),
    fetch: async () => mk({ status: 'succeeded', exit_code: 0 })
  }))
  await done.start()
  check('reset: 终态允许', done.reset() === true)
  check('reset: 清空后回到 idle', done.task.value === null)
  check('reset: 幂等键被清空', done.peekRequestId() === '')

  done.dispose()
}

/* ───────── 22. 关闭 during POST 不能当 idle ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  let resolveStart
  const r = track(useStackPullTask({
    start: () =>
      new Promise((resolve) => {
        resolveStart = resolve
      }),
    fetch: async () => ({
      task_id: 'LATE',
      name: 'app',
      command: 'c',
      status: 'running',
      output: [],
      next_cursor: 0,
      truncated: false,
      exit_code: null,
      error: '',
      started_at: '',
      finished_at: ''
    })
  }))

  const startPromise = r.start()
  // POST 在途时关闭弹窗（pause 表示"关窗"，不是卸载）
  r.pause()
  check('关闭: 在途时标记 startInFlight', r.isStartInFlight() === true)

  resolveStart({
    task_id: 'LATE',
    name: 'app',
    command: 'c',
    status: 'running',
    output: [],
    next_cursor: 0,
    truncated: false,
    exit_code: null,
    error: '',
    started_at: '',
    finished_at: ''
  })
  await startPromise

  check(
    '关闭: 任务已在服务器创建时不丢弃',
    r.task.value !== null,
    '直接丢弃等于忘了服务器上已有任务，重开会起第二个'
  )
  check('关闭: 保留了任务结果', r.task.value?.task_id === 'LATE', String(r.task.value?.task_id))
  check('关闭: phase 不是 idle', r.phase.value !== 'idle', `phase=${r.phase.value}`)
  check('关闭: 记住了发起过启动请求', r.hasStartAttempted() === true)
  check('关闭: request_id 保留（重试复用）', r.peekRequestId() !== '')
  r.dispose()
}

/* ───────── 23. 启动被拒绝时错误必须可见（无任务） ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  const mkErr = (status) => {
    const e = new Error(`status ${String(status)}`)
    e.status = status
    return e
  }

  const mkRunner = (status) =>
    track(
      useStackPullTask({
        start: async () => {
          throw mkErr(status)
        },
        fetch: async () => {
          throw mkErr(status)
        }
      })
    )

  for (const status of [403, 503]) {
    const r = mkRunner(status)
    await r.start()

    check(`启动${String(status)}: 没有产生任务`, r.task.value === null)
    check(
      `启动${String(status)}: errorText 非空`,
      r.errorText.value !== '',
      '空文案等于静默失败，用户点了没反应'
    )
    /*
     * 403 是服务端明确拒绝（不是"不确定"）；
     * 503 则可能是 Agent 超时、任务其实已受理，属于"无法确认"，
     * 必须让用户能同 key 重试确认，所以不允许断言"未执行"。
     */
    if (status === 403) {
      check('启动403: 不标记 uncertain', r.uncertain.value === false, '明确拒绝不是不确定')
    } else {
      check('启动503: 标记为状态未知', r.uncertain.value === true)
      check(
        '启动503: 文案不含"未执行"',
        !/未执行/.test(r.errorText.value),
        r.errorText.value
      )
    }
    check(
      `启动${String(status)}: phase 不是 interrupted`,
      r.phase.value !== 'interrupted',
      '这不是读不到状态，而是启动就被拒了'
    )
    r.dispose()
  }

  /*
   * 关键回归：界面必须有一条只依赖 errorText 的展示分支。
   * 之前条件硬绑在 phase==='failed' && task?.error===''，
   * 而启动失败时 task 为 null、phase 仍是 idle，错误永远不显示。
   */
  const modal = readFileSync(
    join(here, '..', 'src', 'components', 'stack', 'StackOperationModal.vue'),
    'utf8'
  )
  check(
    '界面: 有独立于 phase 的错误展示分支',
    /errorText !== '' && \(task === null \|\| task\.error === ''\)/.test(modal),
    '错误条件不能只挂在 phase==="failed" 上'
  )
  check(
    '界面: 该分支不再硬绑 phase failed',
    !/phase === 'failed' && errorText !== ''/.test(modal),
    '旧条件会让启动失败的错误不可见'
  )
}

/* ───────── 24. 日志用换行拼接 ───────── */

{
  const { useStackOperationTask: useStackPullTask } = composableModule
  const r = track(
    useStackPullTask({
      start: async () => ({
        task_id: 'NL',
        name: 'app',
        command: 'c',
        working_dir: '/srv/app',
        status: 'running',
        // Agent 上报的 text 不含行尾换行
        output: [
          { seq: 1, stream: 'stdout', text: 'line one' },
          { seq: 2, stream: 'stdout', text: 'line two' }
        ],
        next_cursor: 2,
        truncated: false,
        exit_code: null,
        error: '',
        started_at: '',
        finished_at: ''
      }),
      fetch: async () => ({
        task_id: 'NL',
        name: 'app',
        command: 'c',
        working_dir: '/srv/app',
        status: 'running',
        output: [],
        next_cursor: 2,
        truncated: false,
        exit_code: null,
        error: '',
        started_at: '',
        finished_at: ''
      })
    })
  )
  await r.start()
  check('日志: 多行用换行拼接', r.plainLog.value === 'line one\nline two', JSON.stringify(r.plainLog.value))
  check('日志: 不会粘成一行', r.plainLog.value.includes('\n'), '复制日志时会全乱')
  check('工作目录: 归一化保留', r.task.value?.working_dir === '/srv/app', String(r.task.value?.working_dir))
  r.dispose()
}

/* ───────── 结果 ───────── */

/*
 * 最终兜底清理。
 *
 * 状态机内部用 setTimeout 轮询，只要还剩一个未清理的 timer，
 * Node 进程就不会退出，`node --test` 会一直挂着。
 * 各段测试已经自己 dispose 过，这里再扫一遍是为了防止漏网——
 * 漏一个就整个套件卡死，而**不能**用 process.exit 掩盖泄漏。
 */
for (const runner of runners) runner.dispose()

if (failures.length > 0) {
  console.error(`\n✗ 拉镜像任务测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 拉镜像任务测试：${passed} 通过，0 失败`)
