/**
 * 编排迁移契约测试
 *
 * 跑法：node tests/stack-migrate.contract.test.mjs
 *
 * 用与其它测试一致的方式加载真实 `.ts`（transpileModule + data URL），
 * 让 `normalizeOperationTask` / `migrateStack` / 状态机都是**真跑**的，
 * 不是正则匹配源码。
 *
 * 重点钉住三件事：
 * 1. `migrate` 动作能被归一化（否则任务的 action 变 unknown → 被身份校验误拒）；
 * 2. `StackOperationAction` **不含** migrate（不能让编辑动作的枚举被污染）；
 * 3. 两个勾选默认不勾、请求体显式带上两个布尔值与 confirmation；
 * 4. 404 显示为「后端尚未部署」，不是「迁移失败」。
 */
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import ts from 'typescript'

const here = dirname(fileURLToPath(import.meta.url))
const SRC_ROOT = join(here, '..', 'src')
const ROOT = join(here, '..')

const read = (rel) => readFileSync(join(ROOT, rel), 'utf8')

/** 把 .ts 转成可直接 import 的 data URL（复刻其它测试的解析策略） */
async function loadTs(relPath) {
  const src = read(relPath)
  const js = ts.transpileModule(src, {
    compilerOptions: {
      module: ts.ModuleKind.ESNext,
      target: ts.ScriptTarget.ES2022,
      isolatedModules: true
    }
  }).outputText
  const rewritten = js.replace(/from\s+['"]([^'"]+)['"]/g, (whole, spec) => {
    if (spec.startsWith('@/')) {
      const target = spec.slice(2)
      const file = target.endsWith('.ts') ? target : `${target}.ts`
      return `from '${pathToFileURL(join(SRC_ROOT, file)).href}'`
    }
    if (spec.startsWith('.')) {
      return `from '${pathToFileURL(join(dirname(join(ROOT, relPath)), spec)).href}'`
    }
    return `from '${import.meta.resolve(spec)}'`
  })
  return import(`data:text/javascript;base64,${Buffer.from(rewritten, 'utf8').toString('base64')}`)
}

let passed = 0
const failures = []
function check(name, ok, detail) {
  if (ok) passed += 1
  else failures.push(detail === undefined ? name : `${name} —— ${detail}`)
}

const types = await loadTs('src/types/stackOperationTask.ts')

/* ───────── 1. migrate 动作的归一化 ───────── */

{
  const task = types.normalizeOperationTask({
    task_id: 'T1',
    name: 'web',
    action: 'migrate',
    status: 'running',
    output: []
  })
  check('动作: migrate 被识别', task !== null && task.action === 'migrate', JSON.stringify(task?.action))
  check(
    '动作: 任务回显的 name 仍是源编排',
    task !== null && task.name === 'web',
    '后端约定迁移任务 name 为源编排名'
  )
  check(
    '动作: migrate 不在编辑动作枚举里',
    !types.STACK_OPERATION_ACTIONS.includes('migrate'),
    '放进枚举会让所有编辑动作按钮/禁用判断都处理它'
  )
  check('动作: 但它在任务动作集合里', types.STACK_TASK_ACTIONS.includes('migrate'))

  // 未知动作仍归 unknown，不能因为加了 migrate 就放松
  const other = types.normalizeOperationTask({ task_id: 'T2', action: 'teleport', status: 'running' })
  check('动作: 其它未知值仍归 unknown', other !== null && other.action === 'unknown')
}

/* ───────── 2. 请求体语义 ───────── */

{
  // 抓取 httpPost 实际发出的 body
  const captured = []
  const apiSrc = read('src/api/stackOperationTasks.ts')
  const js = ts.transpileModule(apiSrc, {
    compilerOptions: {
      module: ts.ModuleKind.ESNext,
      target: ts.ScriptTarget.ES2022,
      isolatedModules: true
    }
  }).outputText

  /*
   * 用最小桩替换 ./request 与 @/types/api：
   * 这样 migrateStack 的真实分支（字段怎么组装）都会执行。
   */
  const stubs = new Map()
  stubs.set(
    'request',
    `data:text/javascript;base64,${Buffer.from(
      `export async function httpPost(url, body) { globalThis.__MIGRATE_POST__(url, body); return globalThis.__MIGRATE_RES__(); }
       export async function httpGet(url) { globalThis.__MIGRATE_GET__(url); return globalThis.__MIGRATE_RES__(); }`,
      'utf8'
    ).toString('base64')}`
  )
  stubs.set(
    'api-types',
    `data:text/javascript;base64,${Buffer.from(
      'export class ApiError extends Error { constructor(status, code, message) { super(message); this.status = status; this.code = code } }',
      'utf8'
    ).toString('base64')}`
  )

  const rewritten = js.replace(/from\s+['"]([^'"]+)['"]/g, (whole, spec) => {
    if (spec === './request') return `from '${stubs.get('request')}'`
    if (spec === '@/types/api') return `from '${stubs.get('api-types')}'`
    if (spec === '@/types/stackOperationTask') {
      return `from '${pathToFileURL(join(SRC_ROOT, 'types/stackOperationTask.ts')).href}'`
    }
    if (spec.startsWith('.')) {
      return `from '${pathToFileURL(join(SRC_ROOT, 'api', spec)).href}'`
    }
    return `from '${import.meta.resolve(spec)}'`
  })

  globalThis.__MIGRATE_POST__ = (url, body) => captured.push({ url, body })
  globalThis.__MIGRATE_RES__ = () => ({
    task: { task_id: 'M1', name: 'web', action: 'migrate', status: 'queued', output: [] }
  })

  const api = await import(
    `data:text/javascript;base64,${Buffer.from(rewritten, 'utf8').toString('base64')}`
  )

  const task = await api.migrateStack('agent/src', 'my stack', 'req-1', {
    target_agent_id: 'agent/dst',
    include_volumes: false,
    start_after: false,
    confirmation: true
  })

  check('API: 返回归一化的任务', task.task_id === 'M1' && task.action === 'migrate')
  check('API: 只发一次 POST', captured.length === 1, String(captured.length))

  const { url, body } = captured[0]
  check(
    'API: 路径是独立 migrate 端点且做了编码',
    url === '/agents/agent%2Fsrc/stacks/my%20stack/migrate',
    url
  )
  check('API: body 带 target_agent_id', body.target_agent_id === 'agent/dst')
  check(
    'API: include_volumes 显式发送 false',
    body.include_volumes === false,
    '省略等于让后端猜用户的选择'
  )
  check(
    'API: start_after 显式发送 false',
    body.start_after === false,
    '省略等于让后端猜用户的选择'
  )
  check('API: confirmation 为 true', body.confirmation === true)
  check('API: body 带 request_id', body.request_id === 'req-1')

  // 勾上时也要如实发送
  captured.length = 0
  await api.migrateStack('a', 's', 'req-2', {
    target_agent_id: 'b',
    include_volumes: true,
    start_after: true,
    confirmation: true
  })
  check('API: 勾选后发送 true', captured[0].body.include_volumes === true && captured[0].body.start_after === true)

  /* 404 判定 */
  check('404: 识别为未部署', api.isMigrateNotDeployed({ status: 404 }) === true)
  check('404: 403 不算未部署', api.isMigrateNotDeployed({ status: 403 }) === false, '权限问题要如实说')
  check('404: 网络错误不算未部署', api.isMigrateNotDeployed(new Error('fetch failed')) === false)
  check('404: null 不算', api.isMigrateNotDeployed(null) === false)
}

/* ───────── 3. 状态机：迁移任务用同一套机器，身份校验能过 ───────── */

{
  const { useStackOperationTask } = await loadTs('src/composables/useStackOperationTask.ts')
  const runners = []

  function mk(startImpl, fetchImpl, opts = {}) {
    const r = useStackOperationTask({
      start: startImpl,
      fetch: fetchImpl,
      expectedName: 'web',
      expectedAction: 'migrate',
      savesConfig: false,
      ...opts
    })
    runners.push(r)
    return r
  }

  // 正常路径：POST 返回 migrate 任务 → 被接受并开始轮询
  const okTask = types.normalizeOperationTask({
    task_id: 'M1',
    name: 'web',
    action: 'migrate',
    status: 'running',
    output: [{ seq: 1, stream: 'command', text: 'docker save -o /tmp/x.tar img' }],
    next_cursor: 1
  })
  const good = mk(
    async () => okTask,
    async () => okTask
  )
  await good.start()
  check('状态机: 接受 migrate 初响应', good.task.value !== null && good.phase.value === 'running', good.phase.value)
  check('状态机: 日志已收下', good.commandCount.value === 1, String(good.commandCount.value))

  // 身份不符：name 不对 → 拒绝，且错误可见
  const wrongName = mk(
    async () =>
      types.normalizeOperationTask({ task_id: 'X1', name: 'other', action: 'migrate', status: 'running' }),
    async () => types.normalizeOperationTask({ task_id: 'X1', name: 'other', action: 'migrate' })
  )
  await wrongName.start()
  check(
    '状态机: 别的编排的响应被拒',
    wrongName.task.value === null && wrongName.errorText.value !== '',
    '串项目时把别人的日志显示成自己的更糟'
  )

  // 动作不符：同一编排的 pull 日志不能显示成迁移
  const wrongAction = mk(
    async () =>
      types.normalizeOperationTask({ task_id: 'Y1', name: 'web', action: 'pull', status: 'running' }),
    async () => types.normalizeOperationTask({ task_id: 'Y1', name: 'web', action: 'pull' })
  )
  await wrongAction.start()
  check(
    '状态机: 别的动作的响应被拒',
    wrongAction.task.value === null && wrongAction.errorText.value !== '',
    '同一编排可以先后跑 pull 和 migrate，日志绝不能串'
  )

  // 503：非保存类 → 状态未知，不声称已保存
  const r503 = mk(
    async () => {
      const err = new Error('unavailable')
      err.status = 503
      throw err
    },
    async () => okTask
  )
  await r503.start()
  check('状态机: 503 不声称已保存', r503.savedNotDeployed.value === false, '迁移不保存配置')
  check('状态机: 503 标记状态未知', r503.uncertain.value === true)
  check('状态机: 503 文案说无法确认', /无法确认/.test(r503.errorText.value), r503.errorText.value)

  for (const r of runners) r.dispose()
}

/* ───────── 4. 组件：默认值、说明四点、无百分比、日志区语义 ───────── */

{
  const modal = read('src/components/stack/StackMigrateModal.vue')

  check('弹窗: 两个勾选默认 false', /const includeVolumes = ref\(false\)/.test(modal) && /const startAfter = ref\(false\)/.test(modal))
  check(
    '弹窗: 含卷勾选框文案',
    /同时迁移数据卷/.test(modal),
    '用户要能一眼看到这个选项'
  )
  check(
    '弹窗: 自动启动勾选框文案',
    /迁移完成后自动启动编排/.test(modal)
  )
  check(
    '弹窗: 不含卷的小字说明写清了目标上卷是空的',
    /数据卷是空的/.test(modal),
    '不勾时数据不会过去，必须明说'
  )
  check(
    '弹窗: 自动启动的小字说明',
    /容器不会运行/.test(modal)
  )

  // 四点说明来自常量，逐条核对内容存在
  check(
    '说明: 含「不含数据卷」',
    types.STACK_MIGRATE_NOTES.some((n) => /不含数据卷/.test(n))
  )
  check(
    '说明: 含「bind 挂载不迁移」',
    types.STACK_MIGRATE_NOTES.some((n) => /宿主机目录绑定/.test(n) && /不会迁移/.test(n))
  )
  check(
    '说明: 含「目标同名会被拒」',
    types.STACK_MIGRATE_NOTES.some((n) => /同名编排会被拒绝/.test(n))
  )
  check(
    '说明: 含「复制不是移动」',
    types.STACK_MIGRATE_NOTES.some((n) => /复制不是移动/.test(n) && /不会停止/.test(n))
  )
  check('说明: 恰好四条', types.STACK_MIGRATE_NOTES.length === 4, String(types.STACK_MIGRATE_NOTES.length))

  // 没有百分比：迁移没有可靠总进度，硬造会骗人
  check('弹窗: 不出现百分比进度', !/%/.test(modal.replace(/\/\*[^]*?\*\//g, '')), '日志是文本流，不该有百分比')
  check('弹窗: 不引入 n-progress', !/<n-progress/.test(modal))

  // 日志区安全渲染
  check('安全: 无 v-html 赋值', !/v-html\s*=/.test(modal))
  check('安全: 有 role=log', /role="log"/.test(modal))
  check('安全: aria-live', /aria-live="polite"/.test(modal))
  check(
    '安全: 输出走 pre 插值',
    /<pre v-if="[^"]*"\s+class="migrate-modal__lines">\{\{[\s\S]{0,200}?join\('\\n'\)[\s\S]{0,60}?\}\}<\/pre>/.test(modal),
    '日志必须用文本插值渲染，绝不能拼 HTML'
  )
  check('安全: 命令用 code 展示', /<code class="migrate-modal__step-cmd">/.test(modal))
  check(
    '安全: 不提供可编辑命令输入',
    !/<n-input[\s\S]{0,200}?command/.test(modal)
  )
  check(
    '安全: stage 单独弱化样式且不加 $ 前缀',
    /class="migrate-modal__stage"/.test(modal) && !/\$ \{\{/.test(modal)
  )

  // 未部署提示
  check('未部署: 有专门提示', /迁移功能的后端接口尚未部署/.test(modal))
  check(
    '未部署: 不显示成迁移失败',
    /notDeployed/.test(modal) && /!notDeployed/.test(modal),
    '404 时不该再叠一条红色"迁移失败"'
  )

  /*
   * 未勾选「同时迁移数据卷」时，必须有独立的一句「目标上数据卷是空的」。
   * 这是本次需求的核心差别：用户天然以为"迁移"会带上数据，
   * 只在勾选时才说明等于默认状态下最有误导。
   */
  check(
    '不含卷: 未勾选时也有明确说明',
    /v-else[^>]*>\s*<strong>目标服务器上的数据卷是空的。<\/strong>/.test(modal),
    '默认状态必须明说数据不会过去'
  )
  check(
    '不含卷: 说明出现在默认（未勾选）分支',
    /v-if="includeVolumes"[\s\S]{0,400}?v-else[\s\S]{0,200}?数据卷是空的/.test(modal)
  )

  /* 关闭语义：迁移是长任务，关窗不能让用户以为取消了 */
  check(
    '关闭: 执行中按钮文案说明迁移继续',
    /关闭窗口（迁移继续）/.test(modal),
    '用户点关闭不该以为取消了迁移'
  )
  check(
    '关闭: 有文字说明可在迁移按钮重开',
    /关闭窗口不会中断迁移/.test(modal)
  )

  /* 服务器列表兜底 */
  check(
    '列表: 开窗时兜底加载',
    /void ensureAgents\(\)/.test(modal) && /agentsStore\.refresh\(true\)/.test(modal)
  )
  check(
    '列表: 刷新失败静默',
    /catch \{[\s\S]{0,120}?\}/.test(modal) && /静默/.test(modal),
    '列表加载失败不该让迁移弹窗报错弹死'
  )
  check(
    '列表: 加载中与空态区分',
    /refreshingAgents/.test(modal) && /正在加载服务器列表/.test(modal) && /没有其它可选的服务器/.test(modal),
    '空态显示成"暂无"会让用户以为功能坏了'
  )
  check(
    '列表: 下拉带 loading',
    /:loading="refreshingAgents"/.test(modal)
  )

  // 目标下拉：排除源 + 离线不可选
  check('目标: 排除源服务器', /filter\(\(a\) => a\.id !== props\.agentId\)/.test(modal))
  check('目标: 离线项禁用', /disabled: !a\.online/.test(modal))
  check('目标: 离线项标注', /（离线）/.test(modal))

  // 关窗不销毁任务
  check(
    '关闭: 只 pause 不取消',
    /function close\(\): void \{[\s\S]{0,120}?runner\.pause\(\)/.test(modal),
    '关窗后任务在服务器上继续跑，重开要能看到同一份进度'
  )
  check('重开: 有任务则 resume', /void runner\.resume\(\)/.test(modal))
  check(
    '入口: watch 是 immediate',
    /watch\(\(\) => props\.show, onShow, \{ immediate: true \}\)/.test(modal),
    '父级常驻实例 + show 初始 true 时非 immediate 不会触发'
  )
}

/* ───────── 5. composable：复用状态机而非重写 ───────── */

{
  const comp = read('src/composables/useStackMigrateTask.ts')
  check('复用: 引用 useStackOperationTask', /import \{ useStackOperationTask \} from '\.\/useStackOperationTask'/.test(comp))
  check('复用: 展开其返回值', /\.\.\.runner/.test(comp))
  check('复用: 不自己实现轮询', !/setTimeout|POLL_INTERVAL/.test(comp), '状态机只该有一份')
  check('复用: 进度复用操作任务读取', /getStackOperationTask/.test(comp))
  check('身份: 期望动作是 migrate', /expectedAction: 'migrate'/.test(comp))

  /*
   * 下面两条是身份维度最容易写错的：任务归属源服务器与源编排。
   * 写成目标的名字/目标 agent 会被状态机判定为"不属于当前操作"而拒掉第一份响应。
   */
  check(
    '身份: 期望名是源编排',
    /expectedName: options\.stackName/.test(comp),
    '任务回显的 name 是源编排名'
  )
  check(
    '身份: fetch 从源服务器拉进度',
    /getStackOperationTask\(options\.agentId, options\.stackName/.test(comp),
    '源是执行编排、持有任务的一方'
  )
  check(
    '身份: start 也打源服务器',
    /migrateStack\(options\.agentId, options\.stackName/.test(comp)
  )
  check(
    '身份: 不用目标 agent 读取或校验',
    !/options\.targetAgentId|params\.targetAgentId\(\)\s*,\s*after/.test(comp),
    '目标 id 只该进请求体，不该参与读取与身份校验'
  )
  check('503: 不按保存类处理', /savesConfig: false/.test(comp))
  check('404: 在 start 闭包里拦截', /isMigrateNotDeployed\(err\)/.test(comp))
}

/* ───────── 6. 按钮接线与互斥 ───────── */

{
  const actions = read('src/components/stack/ManagedStackActions.vue')

  check('按钮: 存在迁移按钮文案', /迁移\n/.test(actions) || /迁移<\/n-button>/.test(actions))
  check('按钮: 点击走 handleMigrate', /@click="handleMigrate"/.test(actions))
  check('按钮: 二次确认', /await confirmAction\(\{[\s\S]{0,400}?迁移/.test(actions))
  check(
    '互斥: 有任务时禁用迁移',
    /const migrateDisabled = computed\(\(\) => \{[\s\S]{0,200}?anyBusy\.value/.test(actions)
  )
  check(
    '互斥: 迁移中锁住其它动作',
    /if \(migrateBusy\.value\) return true/.test(actions),
    '迁移是重操作，不能和启动/重建/删除并行'
  )
  check('接线: 使用迁移弹窗', /<stack-migrate-modal/.test(actions))
  check(
    '接线: 迁移弹窗常驻不 v-if',
    !/<stack-migrate-modal[\s\S]{0,80}?\bv-if=/.test(actions),
    'v-if 关闭会卸载，任务与 request_id 一起丢'
  )
  check('接线: 目标来自 agents store', /useAgentsStore/.test(actions))
  check('接线: 迁移结束刷新父级', /@finished="handleFinished"/.test(actions))
}

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 迁移契约测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 迁移契约测试：${passed} 通过，0 失败`)