/**
 * 真实响应契约测试（纯 Node，无需测试框架）
 *
 * ## 这个测试要解决什么
 *
 * 之前 external_stacks 的字段被前端写错（写成 containers_running/containers_total，
 * 真实是 running/total），结果界面上显示 `undefined` —— 而当时的测试**全绿**，
 * 因为测试用的是前端自己编的 mock 数据，等于拿自己的想象证明自己对。
 *
 * 所以这里的输入**必须**是后端真实响应样本（tests/fixtures/real-hub-responses.json，
 * 每个字段都标注了对应的 handler 函数），断言也只断言「页面实际会用到的东西」：
 *   - 不该出现 undefined / NaN；
 *   - 解包后的层级正确（stack 里的字段被提到顶层）；
 *   - 动作名确实被翻译成后端认识的值；
 *   - 畸形响应也有兜底，不炸。
 *
 * ## 怎么做（不引依赖）
 *
 * api/stacks.ts 依赖 ./request 的 4 个 http 函数。这里用 Node 的模块拦截（registerHooks）
 * 把 `@/api/request` 解析到一个假的、可控返回值的模块上，然后**直接调用真实的
 * api/stacks.ts**。这样测的是真实解包逻辑，而不是它的复刻品。
 *
 * 运行：node tests/contract.test.mjs
 */

import { registerHooks } from 'node:module'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const fixture = JSON.parse(readFileSync(join(here, 'fixtures', 'real-hub-responses.json'), 'utf8'))

/**
 * 当前要被假 request 模块返回的响应。
 * 每个用例开始前改它，调用真实 api 函数后断言结果。
 */
let nextResponse = null
/** 记录最后一次请求，用于断言「实际发出去的是什么」 */
let lastCall = null

/**
 * 用 Node 的模块钩子把 `@/api/request` 替换成假实现。
 *
 * 为什么能拦到 `@/...`：api/stacks.ts 里是 `from './request'`，
 * 而这里拦截的是解析后的真实文件路径。
 */
const requestStubSource = `
export async function httpGet(url) { return globalThis.__ARKADE_STUB__.get(url) }
export async function httpPost(url, body) { return globalThis.__ARKADE_STUB__.post(url, body) }
export async function httpPut(url, body) { return globalThis.__ARKADE_STUB__.put(url, body) }
export async function httpDelete(url) { return globalThis.__ARKADE_STUB__.del(url) }
`

const requestPath = join(here, '..', 'src', 'api', 'request.ts')

registerHooks({
  resolve(specifier, context, nextResolve) {
    // 把 api/stacks.ts 里的 './request' 指到假模块
    if (specifier === './request' && context.parentURL?.includes('/api/stacks.ts')) {
      return { url: 'stub:request', shortCircuit: true }
    }
    return nextResolve(specifier, context)
  },
  load(url, context, nextLoad) {
    if (url === 'stub:request') {
      return { format: 'module', source: requestStubSource, shortCircuit: true }
    }
    return nextLoad(url, context)
  }
})

globalThis.__ARKADE_STUB__ = {
  get: async (url) => {
    lastCall = { method: 'GET', url }
    return nextResponse
  },
  post: async (url, body) => {
    lastCall = { method: 'POST', url, body }
    return nextResponse
  },
  put: async (url, body) => {
    lastCall = { method: 'PUT', url, body }
    return nextResponse
  },
  del: async (url) => {
    lastCall = { method: 'DELETE', url }
    return nextResponse
  }
}

// 极简断言
let passed = 0
const failures = []

function check(label, condition, detail) {
  if (condition) {
    passed += 1
  } else {
    failures.push(`${label}${detail === undefined ? '' : ` —— ${detail}`}`)
  }
}

function noUndefined(label, object) {
  const bad = Object.entries(object)
    .filter(([, v]) => v === undefined)
    .map(([k]) => k)
  check(label, bad.length === 0, bad.length > 0 ? `以下字段是 undefined: ${bad.join(', ')}` : '')
}

function noNaN(label, value) {
  check(label, !(typeof value === 'string' && value.includes('NaN')), `实际值: ${value}`)
}

// vue 的类型别名 @/types 在运行时不存在，所以这里只 import api 层；
// 它内部只 import type，运行时会被擦除，不会真的加载 types 文件。
const api = await import('../src/api/stacks.ts')

// ───────────────────────── listStacks ─────────────────────────
nextResponse = fixture.listStacks
const list = await api.listStacks('ag-1')

check('listStacks: 路径正确', lastCall.url === '/agents/ag-1/stacks', lastCall.url)
// 断言具体名字而不是条数：加 fixture 用例是常事，钉死数字会让无关改动误报
check(
  'listStacks: stacks 被解成数组并全部保留',
  Array.isArray(list.stacks) &&
    ['my-app', 'busy-app'].every((n) => list.stacks.some((s) => s.name === n)),
  Array.isArray(list.stacks) ? list.stacks.map((s) => s.name).join(',') : String(list.stacks)
)
check('listStacks: agent_online 透传', list.agent_online === true)
check('listStacks: 缺 orphans 时不炸', Array.isArray(list.external_stacks))

const [s0, sBusy] = list.stacks
noUndefined('listStacks: 编排项无 undefined 字段', s0)
check('listStacks: services 是数组', Array.isArray(s0.services))
check('listStacks: services 内容正确', s0.services.join(',') === 'web,db', String(s0.services))
check('listStacks: running 是数字', s0.running === 2, String(s0.running))
check('listStacks: total 是数字', s0.total === 2, String(s0.total))
check('listStacks: version 保留', s0.version === 3)

/*
 * busy 必须原样透传，绝不能在前端被改写成 running 或 stopped。
 * 它是「正在操作、读到的是半写状态」的第三态：
 * 写成 running 会让人以为一切正常，写成 stopped 会让人去点启动，
 * 从而与 Agent 正在执行的操作互相踩。
 */
check('listStacks: busy 状态原样保留', sBusy.status === 'busy', String(sBusy.status))

// external_stacks 是这次修的重点：字段必须是 running/total，不是 containers_*
const [e0] = list.external_stacks
noUndefined('listStacks: external 项无 undefined 字段', e0)
check('listStacks: external.services 是数组', Array.isArray(e0.services), String(e0.services))
check('listStacks: external.running 正确', e0.running === 1, String(e0.running))
check('listStacks: external.total 正确', e0.total === 1, String(e0.total))
check('listStacks: external.managed 是布尔', e0.managed === false)
// 若前端又写回 containers_running，这里会直接被抓出来
check(
  'listStacks: external 不含伪造的 containers_* 字段',
  !('containers_running' in e0) && !('containers_total' in e0)
)

/*
 * 自动接管状态归一化。
 *
 * 核心原则：旧 Hub 不返回该字段时必须落到 unknown，
 * 界面据此完全不显示接管文案。绝不能推测成 skipped/failed——
 * 那会让升级前的 Hub 满屏假原因。
 */
check(
  'auto_adopt: 缺字段 → unknown（不推测）',
  e0.auto_adopt_status === 'unknown',
  String(e0.auto_adopt_status)
)
check('auto_adopt: 缺字段时 reason 为空串', e0.auto_adopt_reason === '')
noUndefined('auto_adopt: 归一化后字段都不是 undefined', {
  status: e0.auto_adopt_status,
  reason: e0.auto_adopt_reason
})

// 已知状态必须原样保留，不能被兜底吃掉
const extByStatus = new Map((list.external_stacks ?? []).map((e) => [e.name, e]))
check(
  'auto_adopt: skipped 原样保留',
  extByStatus.get('legacy-skip')?.auto_adopt_status === 'skipped',
  String(extByStatus.get('legacy-skip')?.auto_adopt_status)
)
check(
  'auto_adopt: skipped 带原因',
  extByStatus.get('legacy-skip')?.auto_adopt_reason === '项目使用了 build，不支持自动接管'
)
check(
  'auto_adopt: failed 原样保留',
  extByStatus.get('legacy-fail')?.auto_adopt_status === 'failed',
  String(extByStatus.get('legacy-fail')?.auto_adopt_status)
)
check(
  'auto_adopt: pending 原样保留',
  extByStatus.get('legacy-pending')?.auto_adopt_status === 'pending',
  String(extByStatus.get('legacy-pending')?.auto_adopt_status)
)
// 后端未来新增的枚举值 → 降级 unknown，而不是原样透传成英文
check(
  'auto_adopt: 未知枚举值降级 unknown',
  extByStatus.get('legacy-weird')?.auto_adopt_status === 'unknown',
  String(extByStatus.get('legacy-weird')?.auto_adopt_status)
)
check(
  'auto_adopt: unknown 时不保留 reason（避免显示假原因）',
  extByStatus.get('legacy-weird')?.auto_adopt_reason === '',
  JSON.stringify(extByStatus.get('legacy-weird')?.auto_adopt_reason)
)

// ───────────────────────── getStack ─────────────────────────
nextResponse = fixture.getStack
const detail = await api.getStack('ag-1', 'my-app')
check('getStack: 路径正确', lastCall.url === '/agents/ag-1/stacks/my-app', lastCall.url)
// 关键：后端包在 {stack} 里，解包后 name 必须能直接读到
check('getStack: 从 stack 包装里解出 name', detail.name === 'my-app', String(detail.name))
check('getStack: 解出 status', detail.status === 'running', String(detail.status))
check('getStack: 解出 services', Array.isArray(detail.services) && detail.services.join(',') === 'web,db')
check('getStack: 后端不再返回 content，不伪造空串', detail.content === undefined)
// 真实字段齐全时必须原样保留数字，不能被「兜底」改成 null/0
check('getStack: 真实 running 保留为数字', detail.running === 2, String(detail.running))
check('getStack: 真实 total 保留为数字', detail.total === 2, String(detail.total))
noUndefined('getStack: 无 undefined 字段（content 除外）', {
  name: detail.name,
  status: detail.status,
  services: detail.services,
  running: detail.running,
  total: detail.total
})

// ───────────────────────── getStackConfig ─────────────────────────
nextResponse = fixture.getStackConfig
const config = await api.getStackConfig('ag-1', 'my-app')
check('getStackConfig: 路径正确', lastCall.url === '/agents/ag-1/stacks/my-app/config', lastCall.url)
check('getStackConfig: content 取到', config.content.includes('nginx'), config.content)
check('getStackConfig: env_content 取到', config.env_content === 'DB_PASSWORD=s3cret\n', config.env_content)
check('getStackConfig: version 取到', config.version === 3, String(config.version))

// ───────────────────────── createStack ─────────────────────────
nextResponse = fixture.createStack
const created = await api.createStack('ag-1', { name: 'new-app', content: 'x', env_content: '' })
check(
  'createStack: 请求带上了 env_content（含空串）',
  'env_content' in (lastCall.body ?? {}),
  JSON.stringify(lastCall.body)
)
check('createStack: 从 stack 里取到 name', created.stack.name === 'new-app', String(created.stack.name))
check('createStack: sync_ok=false 被保留', created.sync_ok === false)
check('createStack: sync_hint 被保留', typeof created.sync_hint === 'string' && created.sync_hint.length > 0)
check('createStack: 未同步时 version 仍是 1', created.stack.version === 1)

// ───────────────────────── updateStack ─────────────────────────
nextResponse = fixture.updateStack
const updated = await api.updateStack('ag-1', 'my-app', { content: 'x', version: 3 })
// 关键：版本号在 stack 里，不在顶层（之前 DetailView 读 res.version 全是 undefined）
check('updateStack: 版本号从 stack.version 读', updated.stack.version === 4, String(updated.stack.version))
check('updateStack: 顶层没有伪造的 version', updated.version === undefined)
check('updateStack: sync_ok=true', updated.sync_ok === true)
check(
  'updateStack: 未传 env_content 时请求体里不含该键（省略=保留后端值）',
  !('env_content' in (lastCall.body ?? {})),
  JSON.stringify(lastCall.body)
)

// ───────────────────────── runStackAction 动作名映射 ─────────────────────────
// 后端的 switch 只认 up/down/stop/restart/pull/rebuild/remove/backup
const expectedActions = {
  start: 'up',
  stop: 'stop',
  restart: 'restart',
  pull: 'pull',
  recreate: 'rebuild',
  backup: 'backup',
  delete: 'remove'
}
const backendAccepted = new Set(['up', 'down', 'stop', 'restart', 'pull', 'rebuild', 'remove', 'backup'])

for (const [front, backend] of Object.entries(expectedActions)) {
  nextResponse = fixture.actionResponse
  await api.runStackAction('ag-1', 'my-app', { action: front })
  check(
    `动作映射: ${front} → ${backend}`,
    lastCall.body?.action === backend,
    `实际发出 ${String(lastCall.body?.action)}`
  )
  // 这条更重要：发出去的值必须是后端 switch 里真实存在的分支，否则会落到 default 报错
  check(
    `动作映射: ${front} 发出的是后端认识的值`,
    backendAccepted.has(lastCall.body?.action),
    `后端不认识 ${String(lastCall.body?.action)}`
  )
  check(
    `动作映射: ${front} 路径正确`,
    lastCall.url === '/agents/ag-1/stacks/my-app/actions',
    lastCall.url
  )
}

// ───────────────────────── deleteStack ─────────────────────────
nextResponse = fixture.deleteStack
const del = await api.deleteStack('ag-1', 'my-app')
// 后端 DELETE 返回 200 + JSON，不是 204
check('deleteStack: 默认 remove_volumes=false 显式发出', lastCall.url.includes('remove_volumes=false'), lastCall.url)
check('deleteStack: 解析出 sync_ok', del.sync_ok === true)

nextResponse = fixture.deleteStack
await api.deleteStack('ag-1', 'my-app', true)
check('deleteStack: 勾选删卷时传 true', lastCall.url.includes('remove_volumes=true'), lastCall.url)

// ───────────────────────── previewImport ─────────────────────────
nextResponse = fixture.previewImport
const preview = await api.previewImport('ag-1', { name: 'act-runner', content: 'x', env_content: '' })
check('previewImport: 路径正确', lastCall.url === '/agents/ag-1/stacks/import-preview', lastCall.url)
check('previewImport: can_import=false', preview.can_import === false)
check('previewImport: blockers 取到', preview.blockers.length === 1, JSON.stringify(preview.blockers))
check('previewImport: warnings 取到', preview.warnings.length === 1)
check('previewImport: config_hash 取到', preview.config_hash === 'pv1')

// ───────────────────────── importStack ─────────────────────────
nextResponse = fixture.importStack
const imported = await api.importStack('ag-1', {
  name: 'act-runner',
  content: 'x',
  env_content: '',
  expected_content_hash: 'pv1',
  confirmation: true
})
check('importStack: 路径正确', lastCall.url === '/agents/ag-1/stacks/import', lastCall.url)
check('importStack: 带回 expected_content_hash', lastCall.body?.expected_content_hash === 'pv1')
check('importStack: 带 confirmation=true', lastCall.body?.confirmation === true)
check('importStack: 解出 adopted', imported.adopted === true)
check('importStack: 解出 stack.name', imported.stack.name === 'act-runner', String(imported.stack.name))

// ───────────────────────── 畸形响应兜底 ─────────────────────────
// 关键语义：字段缺失 = **未知**，不是 0，也不是空数组。
// 把未知说成「0 个在跑 / 0 个服务」是伪造结论，用户会去排查根本没坏的机器。
nextResponse = fixture.degraded.listStacks
const degradedList = await api.listStacks('ag-1')
check('降级: stacks 缺失 → 空数组', Array.isArray(degradedList.stacks) && degradedList.stacks.length === 0)
check('降级: agent_online 缺失 → false', degradedList.agent_online === false)

nextResponse = fixture.degraded.getStack
const degradedDetail = await api.getStack('ag-1', 'x')
check('降级: 详情 name 兜底为空串', degradedDetail.name === '', String(degradedDetail.name))
check('降级: 详情 status 兜底 unknown', degradedDetail.status === 'unknown', String(degradedDetail.status))
check('降级: 详情 drift 兜底 false', degradedDetail.drift === false)
check('降级: 详情 version 兜底 0', degradedDetail.version === 0)
// —— 以下四条是本次纠正的核心 ——
check(
  '降级: services 缺失 → null（未知），不是空数组',
  degradedDetail.services === null,
  JSON.stringify(degradedDetail.services)
)
check(
  '降级: running 缺失 → null（未知），不是 0',
  degradedDetail.running === null,
  String(degradedDetail.running)
)
check(
  '降级: total 缺失 → null（未知），不是 0',
  degradedDetail.total === null,
  String(degradedDetail.total)
)
check(
  '降级: 未知时界面会走「—」分支（非数字）',
  typeof degradedDetail.running !== 'number' && typeof degradedDetail.total !== 'number'
)

nextResponse = fixture.degraded.getStackConfig
const degradedConfig = await api.getStackConfig('ag-1', 'x')
check('降级: config.content 兜底空串', degradedConfig.content === '')
check('降级: config.env_content 兜底空串', degradedConfig.env_content === '')
check('降级: config.version 兜底 0', degradedConfig.version === 0)

nextResponse = fixture.degraded.createStack
const degradedCreate = await api.createStack('ag-1', { name: 'x', content: 'y', env_content: '' })
check('降级: create sync_ok 缺失 → false', degradedCreate.sync_ok === false)
check('降级: create stack 兜底对象', typeof degradedCreate.stack === 'object' && degradedCreate.stack !== null)
check('降级: create stack.services 为 null（未知）', degradedCreate.stack.services === null)

nextResponse = fixture.degraded.previewImport
const degradedPreview = await api.previewImport('ag-1', { name: 'x', content: 'y', env_content: '' })
// 拿不到 config_hash 就无法保证导入的是同一份内容，必须判定为不可导入
check('降级: 预检无 config_hash → can_import=false', degradedPreview.can_import === false)
check('降级: 预检 blockers 兜底数组', Array.isArray(degradedPreview.blockers))

nextResponse = fixture.degraded.importStack
const degradedImport = await api.importStack('ag-1', {
  name: 'x',
  content: 'y',
  env_content: '',
  expected_content_hash: 'h',
  confirmation: true
})
check('降级: import adopted 缺失 → false', degradedImport.adopted === false)
check('降级: import sync_ok 缺失 → false', degradedImport.sync_ok === false)

// ───────────────────────── 未同步记录（自动接管失败） ─────────────────────────
/*
 * 自动接管失败时，记录已写进 Hub 但没下发到服务器：synced_at 为 0。
 * 这类记录会出现在 stacks 列表里、详情页也能打开，看起来完全正常——
 * 必须能把它和正常编排区分开，否则用户会对着它点启动/重建。
 */
const stacksByName = new Map((list.stacks ?? []).map((s) => [s.name, s]))
const unsynced = stacksByName.get('adopt-unsynced')
const unsyncedNoHint = stacksByName.get('adopt-unsynced-nohint')

check('未同步: synced_at 为 0 原样保留', unsynced?.synced_at === 0, String(unsynced?.synced_at))
check(
  '未同步: Hub 给的原因原样保留',
  unsynced?.sync_hint === '自动接管失败：连接服务器超时，已排入下一轮重试',
  String(unsynced?.sync_hint)
)
/*
 * Hub 重启后内存里的原因会丢。此时 sync_hint 必须**缺失**而不是被编成空串/默认值，
 * 前端据此退回通用文案——关键是不能因为没原因就当作同步成功。
 */
check(
  '未同步: Hub 重启后无 sync_hint 字段（不编默认值）',
  unsyncedNoHint !== undefined && !('sync_hint' in unsyncedNoHint),
  `sync_hint = ${JSON.stringify(unsyncedNoHint?.sync_hint)}`
)
check('未同步: 无 hint 时 synced_at 仍为 0', unsyncedNoHint?.synced_at === 0)
check('未同步: 仍照常出现在 stacks 里（不隐藏）', unsynced !== undefined && unsyncedNoHint !== undefined)

// 已同步的记录不该被塞上 sync_hint
const syncedOne = stacksByName.get('my-app')
check('未同步: 正常记录不带 sync_hint', syncedOne !== undefined && !('sync_hint' in syncedOne))

// ───────────────────────── 自动接管状态 UI（源码级） ─────────────────────────
/*
 * 没有 DOM 环境跑不了渲染测试，但接管状态的展示规则里有几条
 * 一旦被改回去就是「用户看到假信息」，值得用断言钉住。
 */
const adoptSrc = readFileSync(
  join(here, '..', 'src', 'components', 'stack', 'AutoAdoptStatus.vue'),
  'utf8'
)
const stackListSrc = readFileSync(
  join(here, '..', 'src', 'views', 'StackListView.vue'),
  'utf8'
)

// unknown 必须完全不渲染：它是「旧 Hub 没这功能」，不是一种后端判定
check(
  'auto_adopt: unknown 不渲染任何内容',
  /unknown: null/.test(adoptSrc) && /v-if="meta !== null"/.test(adoptSrc)
)
// 三种状态必须有中文文案，不能把英文枚举直接显示给用户
check('auto_adopt: pending 显示中文', /pending: \{ label: '接管中'/.test(adoptSrc))
check('auto_adopt: skipped 显示中文', /skipped: \{ label: '配置未登记'/.test(adoptSrc))
check('auto_adopt: failed 显示中文', /failed: \{ label: '接管失败'/.test(adoptSrc))
// 原因只对 skipped/failed 展示，pending 没必要、unknown 不允许推测
check(
  'auto_adopt: 原因仅在 skipped/failed 展示',
  /props\.status === 'skipped' \|\| props\.status === 'failed'/.test(adoptSrc)
)
/*
 * 原因不能只靠 hover：手机上没有 hover。
 * 必须是「可见的一行截断文本」，title 只作桌面端补充。
 */
check(
  'auto_adopt: 原因直接可见（不只 tooltip）',
  /<span v-if="showReason"[^>]*>\{\{ reason \}\}<\/span>/.test(adoptSrc),
  '原因被塞进只有 hover 才显示的地方，手机上读不到'
)
check(
  'auto_adopt: 原因一行截断不撑破表格',
  /text-overflow: ellipsis/.test(adoptSrc) && /white-space: nowrap/.test(adoptSrc)
)

// 列表：整列可见性由后端是否给信息决定，旧 Hub 不能多出一整列空白
check(
  'stack 列表: 接管列仅在有非 unknown 行时才显示',
  /const showAutoAdoptColumn = computed/.test(stackListSrc) &&
    /some\(\(s\) => \(s\.auto_adopt_status \?\? 'unknown'\) !== 'unknown'\)/.test(stackListSrc),
  '缺少按行判断的可见性逻辑'
)
check(
  'stack 列表: 列定义里实际使用 showAutoAdoptColumn',
  /\.\.\.\(showAutoAdoptColumn\.value/.test(stackListSrc),
  'computed 没被列定义使用'
)
// 说明文案必须讲清「不重启不重建」，否则用户不敢用自动登记
check(
  'stack 列表: 文案说明不重启不重建',
  /不重启或重建/.test(stackListSrc),
  '缺少「不重启或重建」，用户会担心自动登记有副作用'
)
// 措辞已从「接管」改为「登记配置」：用户看到的是配置有没有记录，不是被谁接管
check('stack 列表: 文案指向下方原因列', /未登记的原因见下方/.test(stackListSrc))
// 手动导入必须保留（自动接管不支持时的 fallback）
check('stack 列表: 保留手动导入入口', /openImport/.test(stackListSrc))

/* ── 未同步状态的可见性（列表 + 详情） ── */
const detailViewSrc = readFileSync(
  join(here, '..', 'src', 'views', 'StackDetailView.vue'),
  'utf8'
)

// 列表：必须有独立「同步」列，未同步用 error 色 tag 而非仅 tooltip
check(
  'stack 列表: 有独立「同步」列',
  /title: '同步'/.test(stackListSrc),
  '未同步只藏在 tooltip 里，用户看不到'
)
check(
  'stack 列表: 未同步显示 error 色 tag',
  /type: 'error'/.test(stackListSrc) && /未同步/.test(stackListSrc)
)
// 有原因就挂 title，没原因也要照样显示 tag（Hub 重启后原因为空不能伪装成功）
check(
  'stack 列表: 有 sync_hint 时展示原因',
  /row\.sync_hint/.test(stackListSrc),
  '没有把 sync_hint 接上'
)

// 详情：常驻警告必须由 synced_at 推出，而不是某次保存动作
check(
  'stack 详情: 常驻未同步警告由 synced_at 推出',
  /const unsyncedHint = computed/.test(detailViewSrc) &&
    /synced_at > 0\) return null/.test(detailViewSrc),
  '常驻警告不是从编排状态推出的，刷新就消失'
)
check('stack 详情: 常驻警告在模板里渲染', /v-if="unsyncedHint !== null"/.test(detailViewSrc))
// 拿不到原因必须退回通用文案，不能既不显示也不提示
check(
  'stack 详情: 无 sync_hint 时退回通用文案',
  /尚未成功下发到服务器/.test(detailViewSrc) && /请检查同步状态/.test(detailViewSrc),
  '缺原因时没有通用兜底文案'
)
/*
 * 通用文案**不能**承诺「后台自动重试」：自动扫描只覆盖外部/Agent 工作区项目，
 * 手工新建的编排不在扫描范围内，那句承诺对它们不成立，会让用户干等。
 *
 * 只检查字符串字面量：源码注释里为了解释这条约束会提到「自动重试」四个字，
 * 拿整份文件匹配会误报。这里把注释剥掉再判断。
 */
const detailCodeOnly = detailViewSrc
  .replace(/\/\*[\s\S]*?\*\//g, '')
  .replace(/\/\/[^\n]*/g, '')
check(
  'stack 详情: 通用文案不承诺自动重试',
  !/自动重试/.test(detailCodeOnly),
  '文案承诺了后台自动重试，但手工新建的编排并不在自动扫描范围内'
)
// 通用文案要明确劝阻启动/重建
check(
  'stack 详情: 通用文案劝阻启动/重建',
  /不要启动或重建/.test(detailViewSrc)
)

// ───────────────────────── 结果 ─────────────────────────
const total = passed + failures.length
if (failures.length > 0) {
  console.error(`\n✗ 契约测试失败 ${failures.length}/${total}\n`)
  for (const f of failures) console.error(`  - ${f}`)
  process.exit(1)
}
console.log(`✓ 契约测试全部通过（${passed}/${total}）`)
