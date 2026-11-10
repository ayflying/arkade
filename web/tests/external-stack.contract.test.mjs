/**
 * 外部（未纳管）编排详情契约测试
 *
 * 纯 Node 环境，不引框架。跑法：node tests/external-stack.contract.test.mjs
 *
 * 覆盖重点（都是「猜错就会误导用户或写坏数据」的地方）：
 * - 接口路径与请求体形状，必须与 Hub 约定逐字一致；
 * - 权限分层：详情/容器 view 可看，配置/manage 才可读，403 是**正常状态**；
 * - 草稿只写 Hub、CAS 版本号从 0 开始；
 * - 登记前的预检与 blockers 无法绕过；
 * - 复制**不承诺**完整迁移（不复制数据/镜像、不启动、保留源）。
 */
import { registerHooks } from 'node:module'
import { readFileSync, existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))

/* ───────── 迷你断言框架 ───────── */

let passed = 0
const failures = []

function check(name, ok, detail) {
  if (ok) {
    passed++
  } else {
    failures.push(detail === undefined ? name : `${name} —— ${detail}`)
  }
}

/* ───────── 把 ./request 换成桩 ───────── */

let lastCall = null
let nextResponse = null

/*
 * 桩模块由 registerHooks 在加载 `api/externalStacks.ts` 时替换掉 `./request`。
 * 桩里所有调用都经过 globalThis.__EXT_STUB__，这样测试文件能直接记录调用，
 * 不需要跨模块传值。
 */
const requestStub = `
function take() {
  const err = globalThis.__EXT_STUB__.error
  globalThis.__EXT_STUB__.error = null
  if (err !== null) throw err
  return globalThis.__EXT_STUB__.response
}

export async function httpGet(url) {
  globalThis.__EXT_STUB__.record({ method: 'GET', url })
  return take()
}
export async function httpPost(url, body) {
  globalThis.__EXT_STUB__.record({ method: 'POST', url, body })
  return take()
}
export async function httpPut(url, body) {
  globalThis.__EXT_STUB__.record({ method: 'PUT', url, body })
  return take()
}
export async function httpDelete(url) {
  globalThis.__EXT_STUB__.record({ method: 'DELETE', url })
  return take()
}
`

/*
 * 用 data: URL 承载桩模块。
 *
 * 试过 `stub:` 自定义协议：resolve 能过，但 load 阶段默认 ESM loader
 * 只接受 file/data/node 三种 scheme，会直接报错。data: 是官方支持的。
 */
const stubUrl = `data:text/javascript;base64,${Buffer.from(requestStub, 'utf8').toString('base64')}`

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === './request' && context.parentURL?.includes('/api/externalStacks.ts')) {
      return { url: stubUrl, shortCircuit: true }
    }
    return nextResolve(specifier, context)
  }
})

globalThis.__EXT_STUB__ = {
  response: null,
  error: null,
  record: (call) => {
    lastCall = call
  }
}

/* ───────── 导入被测模块 ───────── */

const api = await import('../src/api/externalStacks.ts')
const { ApiError } = await import('../src/types/api.ts')

function setResponse(value) {
  globalThis.__EXT_STUB__.response = value
  globalThis.__EXT_STUB__.error = null
}

function setError(err) {
  globalThis.__EXT_STUB__.error = err
}

/* ───────── 详情 ───────── */

setResponse({
  project: {
    name: 'legacy-app',
    status: 'running',
    services: ['web', 'db'],
    running: 2,
    total: 2,
    managed: false,
    auto_adopt_status: 'skipped',
    auto_adopt_reason: '项目使用了 build，不支持自动接管'
  },
  containers: [
    {
      id: 'c1',
      name: 'legacy-app-web-1',
      image: 'nginx',
      state: 'running',
      status: 'Up 2 hours',
      origin: 'discovered',
      stack_id: 'legacy-app',
      created: 1700000000,
      ports: '0.0.0.0:8080->80/tcp'
    }
  ]
})

const detail = await api.getExternalStack('ag-1', 'legacy-app')
check(
  '详情: 路径走 /external-stacks/{name}',
  lastCall.url === '/agents/ag-1/external-stacks/legacy-app',
  String(lastCall.url)
)
check('详情: 方法 GET', lastCall.method === 'GET', String(lastCall.method))
check('详情: 项目名解出', detail.project.name === 'legacy-app', String(detail.project.name))
check('详情: managed 恒为 false', detail.project.managed === false)
check('详情: 自动接管状态透传', detail.project.auto_adopt_status === 'skipped')
check(
  '详情: 自动接管原因透传',
  detail.project.auto_adopt_reason === '项目使用了 build，不支持自动接管',
  String(detail.project.auto_adopt_reason)
)
check('详情: 容器数组解出', Array.isArray(detail.containers) && detail.containers.length === 1)
check('详情: 容器 state 原样保留', detail.containers[0].state === 'running', String(detail.containers[0].state))
check('详情: 容器 stack_id 原样保留', detail.containers[0].stack_id === 'legacy-app')

// 名称必须编码，否则路径里带 / 会打到别的接口
setResponse({ project: { name: 'a/b c' }, containers: [] })
await api.getExternalStack('ag-1', 'a/b c')
check(
  '详情: 编排名做 URL 编码',
  lastCall.url === '/agents/ag-1/external-stacks/a%2Fb%20c',
  String(lastCall.url)
)

// 字段缺失不能变成 undefined 透给模板
setResponse({ project: { name: 'x' } })
const sparse = await api.getExternalStack('ag-1', 'x')
check('详情: 缺 services → null（未知，不是 0 个）', sparse.project.services === null)
check('详情: 缺 containers → 空数组', Array.isArray(sparse.containers) && sparse.containers.length === 0)
check('详情: 缺 state → unknown', sparse.project.status === 'unknown', String(sparse.project.status))

/* ───────── 配置：可读 ───────── */

setResponse({
  readable: true,
  content: 'services:\n  web:\n    image: nginx\n',
  config_hash: 'h1',
  blockers: [],
  draft: { content: 'services: {}\n', source_hash: 'h1', version: 2, updated_at: 1700000000 }
})

const cfg = await api.getExternalStackConfig('ag-1', 'legacy-app')
check(
  '配置: 路径 /external-stacks/{name}/config',
  lastCall.url === '/agents/ag-1/external-stacks/legacy-app/config',
  String(lastCall.url)
)
check('配置: readable 透传', cfg.readable === true)
check('配置: content 透传', (cfg.content ?? '').includes('nginx'))
check('配置: config_hash 透传', cfg.config_hash === 'h1', String(cfg.config_hash))
check('配置: draft 透传', cfg.draft !== undefined && cfg.draft.version === 2)

/* ───────── 配置：读不到要给原因 ───────── */

setResponse({ readable: false, reason: '该项目的 compose 文件已被删除', blockers: [] })
const unreadable = await api.getExternalStackConfig('ag-1', 'gone')
check('配置: readable=false 原样保留', unreadable.readable === false)
check(
  '配置: 读不到时带人话原因',
  unreadable.reason === '该项目的 compose 文件已被删除',
  String(unreadable.reason)
)

/* ───────── 配置：403 是可分类的正常状态 ───────── */

setError(new ApiError(403, 'forbidden', '无权限'))
let forbiddenStatus = 0
try {
  await api.getExternalStackConfig('ag-1', 'legacy-app')
} catch (err) {
  forbiddenStatus = err.status ?? 0
}
check('配置: 403 原样抛出，供页面区分权限问题', forbiddenStatus === 403, String(forbiddenStatus))

/* ───────── 草稿 ───────── */

setResponse({ draft: { content: 'x', source_hash: 'h1', version: 1, updated_at: 1 } })
const saved = await api.saveExternalDraft('ag-1', 'legacy-app', {
  content: 'x',
  source_hash: 'h1',
  expected_version: 0
})
check('草稿: 方法 PUT', lastCall.method === 'PUT', String(lastCall.method))
check(
  '草稿: 路径 /external-stacks/{name}/draft',
  lastCall.url === '/agents/ag-1/external-stacks/legacy-app/draft',
  String(lastCall.url)
)
check('草稿: 首次保存 expected_version 为 0', lastCall.body.expected_version === 0)
check('草稿: 带 source_hash 用于检测磁盘变动', lastCall.body.source_hash === 'h1')
check('草稿: 返回新版本号', saved.draft.version === 1)

// 409 必须让调用方感知（别处改过草稿），不能静默当成功
setError(new ApiError(409, 'conflict', '版本冲突'))
let conflictStatus = 0
try {
  await api.saveExternalDraft('ag-1', 'legacy-app', { content: 'y', source_hash: 'h1', expected_version: 0 })
} catch (err) {
  conflictStatus = err.status ?? 0
}
check('草稿: 版本冲突 409 抛出', conflictStatus === 409, String(conflictStatus))

/* ───────── 应用预检与登记 ───────── */

setResponse({ can_import: false, config_hash: 'h2', blockers: ['配置包含 build'], warnings: [] })
const pre = await api.previewExternalApply('ag-1', 'legacy-app', { content: 'c' })
check('预检: 方法 POST', lastCall.method === 'POST', String(lastCall.method))
check(
  '预检: 路径 /external-stacks/{name}/apply-preview',
  lastCall.url === '/agents/ag-1/external-stacks/legacy-app/apply-preview',
  String(lastCall.url)
)
check('预检: can_import=false 原样保留', pre.can_import === false)
check('预检: blockers 原样保留', pre.blockers.join(',') === '配置包含 build', pre.blockers.join(','))
check('预检: config_hash 带出供登记校验', pre.config_hash === 'h2')

setResponse({ stack: {}, sync_ok: true, adopted: true })
const applied = await api.applyExternalStack('ag-1', 'legacy-app', {
  content: 'c',
  expected_content_hash: 'h2',
  confirmation: true
})
check(
  '登记: 路径 /external-stacks/{name}/apply',
  lastCall.url === '/agents/ag-1/external-stacks/legacy-app/apply',
  String(lastCall.url)
)
check('登记: 必须显式 confirmation', lastCall.body.confirmation === true)
check(
  '登记: 带上预检过的内容哈希（防止登记了没预检过的内容）',
  lastCall.body.expected_content_hash === 'h2',
  String(lastCall.body.expected_content_hash)
)
check('登记: sync_ok 透传', applied.sync_ok === true)

/* ───────── 复制配置：绝不能承诺完整迁移 ───────── */

setResponse({
  stack: {},
  scope: ['config'],
  data_included: false,
  started: false,
  source_preserved: true,
  message: '已复制配置'
})
const copied = await api.copyExternalConfig('ag-1', 'legacy-app', {
  target_agent_id: 'ag-2',
  target_name: 'legacy-app',
  content: 'c',
  confirmation: true
})
check(
  '复制: 路径 /external-stacks/{name}/copy',
  lastCall.url === '/agents/ag-1/external-stacks/legacy-app/copy',
  String(lastCall.url)
)
check('复制: 指向目标服务器与目标名', lastCall.body.target_agent_id === 'ag-2' && lastCall.body.target_name === 'legacy-app')
check('复制: 必须显式 confirmation', lastCall.body.confirmation === true)
check('复制: 只复制配置（scope 恒为 config）', copied.scope.join(',') === 'config', copied.scope.join(','))
check('复制: 明确不含数据', copied.data_included === false)
check('复制: 明确不启动容器', copied.started === false)
check('复制: 明确保留源', copied.source_preserved === true)

// 后端没给 scope 时也不能编出一个"包含一切"的结果
setResponse({ stack: {} })
const copiedMinimal = await api.copyExternalConfig('ag-1', 'x', {
  target_agent_id: 'ag-2',
  target_name: 'x',
  content: 'c',
  confirmation: true
})
check('复制: 缺 scope 时默认只含 config', copiedMinimal.scope.join(',') === 'config')
check('复制: 缺 data_included 时按不含数据处理', copiedMinimal.data_included === false)
check('复制: 缺 started 时按未启动处理', copiedMinimal.started === false)

/* ───────── 草稿在磁盘不可读时必须保住 ───────── */

/* 几个源文件在下面多处断言要用，统一在这里读一次 */
const viewSrc = readFileSync(
  join(here, '..', 'src', 'views', 'ExternalStackDetailView.vue'),
  'utf8'
)
const configPanelSrc = readFileSync(
  join(here, '..', 'src', 'components', 'external', 'ExternalStackConfigPanel.vue'),
  'utf8'
)
const copyModalSrc = readFileSync(
  join(here, '..', 'src', 'components', 'external', 'ExternalStackCopyModal.vue'),
  'utf8'
)
const storeSrc = readFileSync(
  join(here, '..', 'src', 'composables', 'useExternalStack.ts'),
  'utf8'
)

/*
 * 草稿存在 Hub 数据库里，和磁盘文件是两个独立来源。
 * 文件被删/权限变更时，草稿是用户**唯一幸存**的编辑内容——
 * 读失败时把草稿丢掉等于毁掉它。
 */
check(
  '草稿: readable=false 但存在草稿时仍进入可编辑状态',
  /draftOnly\.value = res\.draft !== undefined/.test(storeSrc) &&
    /configState\.value = draftOnly\.value \? 'ready' : 'unreadable'/.test(storeSrc),
  '草稿在读不到磁盘时被丢弃了'
)
check('草稿: 保留草稿内容不新建空草稿', !/draft\.value = null[\s\S]{0,40}readable/.test(storeSrc))
check('草稿: 有专门标记区分"只剩草稿"', /const draftOnly = ref\(false\)/.test(storeSrc))
check(
  '配置面板: 只剩草稿时明确告知服务器文件不可读',
  /服务器上的文件当前不可读/.test(configPanelSrc),
  '用户会以为看到的就是服务器现状'
)
check(
  '配置面板: 只剩草稿时说明内容来自 Hub',
  /下面显示的是保存在 Hub 中的草稿/.test(configPanelSrc)
)

/* ───────── apply 同名冲突要引导去已纳管详情 ───────── */

check(
  '登记: 同名冲突（409）单独识别，不当普通失败',
  /alreadyManaged\.value = true/.test(viewSrc) && /status === 409/.test(viewSrc),
  '同名已登记时只会显示一句错误，用户不知道去哪看'
)
check('登记: 冲突时给出去已纳管详情的入口', /goManagedDetail/.test(viewSrc))
check('登记: 跳转到已纳管详情路由', /agent-stack-detail/.test(viewSrc))

/* ───────── copy 目标校验 ───────── */

check(
  '复制: 离线目标不可选（目标必须在线）',
  /disabled: a\.status !== 'online'/.test(copyModalSrc),
  '离线目标仍可选，点下去才报错'
)
check('复制: 离线目标时不提交', /!targetOffline\.value/.test(copyModalSrc))
check('复制: 离线时给出明确原因', /目标服务器当前离线/.test(copyModalSrc))
check(
  '复制: 说明目标上只是未同步记录且不会自动启动',
  /尚未同步/.test(copyModalSrc) && /不会自动启动/.test(copyModalSrc)
)
check(
  '复制: 目标同名冲突（409）有针对性提示',
  /已存在同名编排/.test(viewSrc),
  '409 只显示原始错误，用户不知道是重名'
)

/* ───────── 复制/登记必须用当前编辑内容，且上下文快照 ───────── */

/*
 * 之前 handleCopy 用 store.rawContent：用户保存了草稿或在编辑器里改了内容，
 * 复制出去的还是磁盘原文——悄悄丢掉用户的修改。draftOnly 时 rawContent 甚至是空串。
 */
check(
  '复制: 使用编辑器当前内容，不是磁盘原文',
  /const content = editorContent\.value/.test(viewSrc),
  '复制仍在用 rawContent，用户编辑/草稿会被丢弃'
)
check(
  '复制: 不再读取 store.rawContent 作为源内容',
  !/content: store\.rawContent\.value/.test(viewSrc) &&
    !/:content="store\.rawContent\.value"/.test(viewSrc)
)
check(
  '配置面板: 向上同步编辑器内容',
  /'update:content'/.test(configPanelSrc) && /update:content/.test(viewSrc),
  '父组件拿不到编辑器实际内容'
)

/*
 * await listAgents / 网络往返期间用户可能切到别的项目。
 * 用实时响应式值就会复制/登记**错误的项目**，所以必须进函数就快照。
 */
check(
  '复制: 快照源 agent 与项目名',
  /const sourceAgentId = agentId\.value/.test(viewSrc) &&
    /const sourceName = stackName\.value/.test(viewSrc),
  '复制用了实时值，切换项目后会复制错的项目'
)
check(
  '复制: 请求使用快照值',
  /copyExternalConfig\(sourceAgentId, sourceName/.test(viewSrc)
)
check(
  '登记: 快照目标 agent 与项目名',
  /const targetAgentId = agentId\.value/.test(viewSrc) &&
    /const targetName = stackName\.value/.test(viewSrc),
  '登记用了实时值，切换项目后会登记错的项目'
)
check(
  '登记: 预检与登记都用快照值',
  /previewExternalApply\(targetAgentId, targetName/.test(viewSrc) &&
    /applyExternalStack\(targetAgentId, targetName/.test(viewSrc)
)

/* ───────── 路由切换必须重新加载 ───────── */

/*
 * Vue 会复用同一组件实例（只有 params 变），onMounted 不会再跑。
 * 不清空+重载的话，页面显示的还是上一个项目的数据。
 */
check(
  '路由: store 监听 id/name 变化并重载',
  /loadDetail\(\)[\s\S]{0,80}loadConfig\(\)/.test(storeSrc) &&
    /nextAgent === prevAgent/.test(storeSrc),
  '路由切换后不会重新加载，会显示上一个项目的数据'
)
check('路由: 视图也监听参数变化重置局部状态', /activeTab\.value = 'overview'/.test(viewSrc))
check('路由: 没有引入轮询', !/usePolling|setInterval/.test(viewSrc) && !/usePolling|setInterval/.test(storeSrc))

/* ───────── 409 不能一律宣称「已登记」 ───────── */

/*
 * 409 可能来自：同名已在 Hub、校验冲突、运行时冲突。前端分不清是哪种，
 * 一律说「已登记」对后两种就是假消息。
 */
check(
  '登记: 409 展示后端原文而不宣称成功',
  /applyError\.value = err\.message === '' \? '登记冲突/.test(viewSrc),
  '409 被直接当成「已登记」，可能是假的'
)
check(
  '登记: 409 提示里不出现「已登记到 Arkade」的断言',
  !/已经登记到 Arkade 了/.test(viewSrc)
)
check('登记: 入口文案是「查看已纳管项目」', /查看已纳管项目/.test(viewSrc))

/* ───────── 复制成功文案不能说过头 ───────── */

check(
  '复制: 只说写入目标 Hub 记录，不说已到服务器',
  /已复制到目标 Hub 记录/.test(viewSrc) && !/配置已复制到目标服务器/.test(viewSrc),
  '文案夸大成已经到目标服务器，实际还没下发'
)
check('复制: 明确未下发且不会自动启动', /尚未下发到目标服务器/.test(viewSrc) && /不会自动启动/.test(viewSrc))

/* ───────── 容器明细表（不能只显示数字） ───────── */

/*
 * 加了组件不等于表格真的渲染出来。这里分两层守：
 * 1. 语义层：组件确实消费了接口返回的字段（名称/镜像/状态/端口/创建时间）；
 * 2. 渲染层：NDataTable / NEmpty 必须显式 import。
 *
 * 第 2 点是有教训的：Naive UI 本项目未全局注册，vue-tsc 对未注册的
 * kebab-case 标签**不报错**，表格会静默渲染成空 DOM。
 * 卷文件管理器就漏过一次，当时的测试只查了源码存在性，没发现界面是空的。
 */
const containersSrc = readFileSync(
  join(here, '..', 'src', 'components', 'external', 'ExternalStackContainers.vue'),
  'utf8'
)

check(
  '容器表: NDataTable 显式 import（否则静默渲染成空）',
  /import \{[^}]*NDataTable[^}]*\} from 'naive-ui'/.test(containersSrc),
  'NDataTable 没有 import，表格会空着且 vue-tsc 不报错'
)
check(
  '容器表: NEmpty 显式 import（空态同样需要注册）',
  /import \{[^}]*NEmpty[^}]*\} from 'naive-ui'/.test(containersSrc)
)
check(
  '容器表: 模板里真的用了 n-data-table 且绑定 data',
  /<n-data-table/.test(containersSrc) && /:data="containers"/.test(containersSrc),
  '组件存在但没渲染表格'
)

// 五个列必须覆盖接口返回的字段，缺一个就是信息丢失
check('容器表: 展示名称', /title: '名称'/.test(containersSrc))
check('容器表: 展示镜像', /title: '镜像'/.test(containersSrc))
check('容器表: 展示状态', /title: '状态'/.test(containersSrc) && /ContainerStateTag/.test(containersSrc))
check('容器表: 展示端口', /title: '端口'/.test(containersSrc))
check('容器表: 展示创建时间', /title: '创建时间'/.test(containersSrc))

// created 是 Unix 秒；用现成工具而不是自己 new Date
check(
  '容器表: 创建时间复用 formatTime 工具',
  /formatAbsoluteTime/.test(containersSrc) && /formatRelativeTime/.test(containersSrc),
  '自己拼时间格式容易把秒当年毫秒，显示成 1970'
)
check(
  '容器表: 复用容器 Container 类型',
  /import type \{ Container \} from '@\/types\/container'/.test(containersSrc)
)
check(
  '容器表: 空态给出明确说明',
  /description="这个编排当前没有容器/.test(containersSrc),
  '空表没说明，用户会以为加载失败'
)
check('容器表: 视图在概览下方接入', /external-stack-containers/.test(viewSrc))

/* ───────── 总览页未纳管告警的措辞与跳转 ───────── */

/*
 * 旧的「未登记因此无法通过 Arkade 管理」在外部项目详情上线后就不成立了：
 * 现在能看容器、日志与配置草稿。文案错了会让用户以为那些项目完全没法看。
 */
const overviewSrc = readFileSync(join(here, '..', 'src', 'views', 'OverviewView.vue'), 'utf8')

check(
  '总览: 不再宣称未纳管项目无法通过 Arkade 管理',
  !/因此无法通过 Arkade 管理/.test(overviewSrc),
  '旧文案仍会说未纳管项目完全无法管理'
)
check(
  '总览: 说明可进入外部项目查看容器/日志/配置草稿',
  /可进入外部项目查看容器、日志与配置草稿/.test(overviewSrc)
)
check(
  '总览: 未纳管项直达外部详情',
  /enterExternalStack\(item\.agentId, item\.stackName\)/.test(overviewSrc) &&
    /agent-stack-external-detail/.test(overviewSrc)
)
// 漂移项仍然只跳列表：它不是外部项目，没有 external 详情页
check(
  '总览: 漂移项仍跳编排列表，不被误改',
  /enterStacks\(item\.agentId\)/.test(overviewSrc)
)

/* ───────── 路由与视图接线（源码级） ───────── */

const routerSrc = readFileSync(join(here, '..', 'src', 'router', 'index.ts'), 'utf8')
const listSrc = readFileSync(join(here, '..', 'src', 'views', 'StackListView.vue'), 'utf8')

check(
  '路由: 外部详情注册为 stacks/external/:name',
  /path: 'stacks\/external\/:name'/.test(routerSrc)
)
// 必须先于 stacks/:name，否则 'external' 会被当成编排名
const externalIdx = routerSrc.indexOf("path: 'stacks/external/:name'")
const genericIdx = routerSrc.indexOf("path: 'stacks/:name'")
check(
  '路由: 外部路由注册在 stacks/:name 之前',
  externalIdx > -1 && genericIdx > -1 && externalIdx < genericIdx,
  `external=${String(externalIdx)} generic=${String(genericIdx)}`
)
check('列表: 外部编排名可点击进详情', /goExternalDetail/.test(listSrc))
check('列表: 跳转到外部详情路由', /agent-stack-external-detail/.test(listSrc))

// 视图必须把 403 当权限提示，而不是加载失败（文案在 composable 里生成）
check('视图: 配置 403 与故障分开处理', /forbidden/.test(storeSrc))
check('视图: 有权限提示文案', /管理权限/.test(storeSrc))

// 不可读时必须显示原因，不能给一个空白编辑器
check(
  '配置面板: 不可读时展示原因',
  /config 内容不可读|配置内容不可读/.test(configPanelSrc) && /reason/.test(configPanelSrc)
)
check(
  '配置面板: 不可读时禁用编辑（canEdit 传入 false）',
  /can-edit="store\.configState\.value === 'ready'"/.test(viewSrc)
)

// 草稿按钮文案与「不写服务器」承诺
check('配置面板: 按钮是「保存草稿」', /保存草稿/.test(configPanelSrc))
check(
  '配置面板: 说明草稿不改服务器文件',
  /不会修改服务器上的文件/.test(configPanelSrc)
)

// blockers 只挡登记，不挡编辑
check('配置面板: blockers 明确不阻止存草稿', /仍然可以继续编辑并保存草稿/.test(configPanelSrc))

// 登记文案不能承诺改变运行状态
check('视图: 动作叫「登记到 Arkade」', /登记到 Arkade/.test(viewSrc))
check('视图: 说明登记不启动不重启', /不启动也不重启/.test(viewSrc))

// 复制弹窗必须列明不含什么，且不叫「迁移」
check('复制弹窗: 标题不叫迁移', /复制配置到其他服务器/.test(copyModalSrc) && !/title="迁移/.test(copyModalSrc))
check('复制弹窗: 明确不是完整迁移', /这不是完整迁移/.test(copyModalSrc))
check('复制弹窗: 列出不复制 .env', /\.env/.test(copyModalSrc))
check('复制弹窗: 列出不复制数据卷', /数据卷/.test(copyModalSrc))
check('复制弹窗: 列出不复制镜像', /镜像/.test(copyModalSrc))
check('复制弹窗: 列出不启动容器', /不启动/.test(copyModalSrc))
check('复制弹窗: 说明保留源', /源服务器上的配置与容器都不受影响/.test(copyModalSrc))

// 日志复用现有容器日志接口，不新增 external/logs 端点
const logsSrc = readFileSync(
  join(here, '..', 'src', 'components', 'external', 'ExternalStackLogs.vue'),
  'utf8'
)
check('日志: 复用容器日志接口', /fetchContainerLogs/.test(logsSrc))
check('日志: 视图通过独立组件接入', /ExternalStackLogs/.test(viewSrc))
check('API: 没有 external/logs 端点', !/\/logs/.test(readFileSync(join(here, '..', 'src', 'api', 'externalStacks.ts'), 'utf8')))

// 不扩已纳管详情页
const detailViewSrc = readFileSync(join(here, '..', 'src', 'views', 'StackDetailView.vue'), 'utf8')
check(
  '不改动已纳管详情页：没有外部项目逻辑',
  !/external-stacks/.test(detailViewSrc) && !/useExternalStack/.test(detailViewSrc)
)

// 视图保持短文件（分模块的验收标准）
const viewLines = viewSrc.replace(/\n$/, '').split('\n').length
check('视图: 外部详情页不超过 420 行', viewLines <= 420, `实际 ${viewLines} 行`)

/* ───────── 结果 ───────── */

const total = passed + failures.length
if (failures.length > 0) {
  console.error(`\n✗ 外部编排契约测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 外部编排契约测试：${passed} 通过，0 失败`)
console.log(`\n✓ 全部通过（${total}/${total}）`)
