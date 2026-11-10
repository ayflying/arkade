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

const { buildStackRows } = await import('../src/utils/stackListModel.ts')

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

// ───────────────────────── Unified list rendering contracts ─────────────────────────
const source = path => readFileSync(join(here, '..', 'src', path), 'utf8')
const stackListSrc = source('views/StackListView.vue')
const tableSrc = source('components/stack/StackUnifiedTable.vue')
const detailViewSrc = source('views/StackDetailView.vue')
const externalSection = source('components/stack/ExternalStackConfigSection.vue')
const rows = buildStackRows(list.stacks, list.external_stacks)
const row = name => rows.find(item => item.name === name)
check('统一列表: 页面消费真实统一模型并接入表格', /buildStackRows\(stacks\.value, externalStacks\.value\)/.test(stackListSrc) && /<stack-unified-table/.test(stackListSrc))
check('统一列表: 独立登记状态列区分未登记与未同步', /title: '登记状态'/.test(tableSrc) && /unsynced: \{ label: '未同步'/.test(tableSrc) && /unregistered: \{ label: '未登记'/.test(tableSrc))
check('统一列表: 未同步状态不依赖原因是否存在', row('adopt-unsynced').regState === 'unsynced' && row('adopt-unsynced-nohint').regState === 'unsynced')
check('统一列表: 保留真实同步原因并补充缺失原因', row('adopt-unsynced').regReason === unsynced.sync_hint && row('adopt-unsynced-nohint').regReason.includes('尚未下发'))
check('统一列表: 正常记录不显示虚假同步原因', row('my-app').regState === 'synced' && row('my-app').regReason === '')
check('统一列表: skipped/failed 原因保留给登记状态展示', row('legacy-skip').regReason === extByStatus.get('legacy-skip').auto_adopt_reason && row('legacy-fail').regReason === extByStatus.get('legacy-fail').auto_adopt_reason)
check('统一列表: unknown 不推测失败原因', row('legacy-weird').regReason === '配置只存在于服务器的原目录，Arkade 中没有记录')
check('统一列表: 原因实际接入登记状态 tooltip', /row\.regReason/.test(tableSrc) && /default: \(\) => h\('div', \{ class: 'stack-unified__tip' \}, row\.regReason\)/.test(tableSrc))
check('统一列表: 所有项目名称进入统一详情', /@open="goDetail"/.test(stackListSrc) && /name: 'agent-stack-detail'/.test(stackListSrc))
check('手动登记: 外部项目配置区保留预检登记 fallback', /@click="handleApply\(editorContent\)"/.test(externalSection) && /登记到 Arkade/.test(externalSection))
check('手动导入: 保留可达的独立导入入口', /StackImportModal|stack-import-modal|openImport/.test(stackListSrc + externalSection))
check('登记: 明确不启动不重启', /不启动也不重启/.test(externalSection))
check('详情: 常驻未同步警告直接由 synced_at 推出', /v-if="managed && managed\.synced_at === 0"/.test(detailViewSrc))
check('详情: 后端同步原因与通用文案均渲染', /managed\.sync_hint \|\|/.test(detailViewSrc) && /尚未成功下发/.test(detailViewSrc) && /请检查同步状态/.test(detailViewSrc))
check('详情: 通用文案劝阻启动重建且不承诺自动重试', /不要启动或重建/.test(detailViewSrc) && !/自动重试/.test(detailViewSrc.replace(/<!--[^]*?-->/g, '')))
check('详情: 容器获取失败显示警告而非空容器结论', /v-if="detail\.containersError\.value"/.test(detailViewSrc) && /<stack-containers-table v-else/.test(detailViewSrc))

// Managed configuration and operations retain security/confirmation contracts after extraction.
const managedConfigSrc = source('composables/useManagedStackConfig.ts')
const managedPanelSrc = source('components/stack/ManagedStackConfigPanel.vue')
const managedActionsSrc = source('components/stack/ManagedStackActions.vue')
check('配置: env 三态通过真实 payload builder，未变省略/空串清空/新值替换', /env !== originalEnv \? \{ env_content: env \} : \{\}/.test(managedConfigSrc) && /managedUpdatePayload\(content.value, version.value, envContent.value, originalEnv.value\)/.test(managedConfigSrc))
check('配置: env 编辑器仍接入且保存时锁定', /<stack-env-editor v-model="config.envContent.value" :disabled="config.saving.value"/.test(managedPanelSrc))
check('配置: manage 403 不影响容器与日志', /err.status === 403/.test(managedConfigSrc) && /容器与日志仍可查看/.test(managedConfigSrc))
check('配置: 保存需确认 compose up 部署而非草稿', /await confirmAction/.test(managedConfigSrc) && /立即执行 compose up/.test(managedConfigSrc) && /danger: true/.test(managedConfigSrc))
check('配置: CAS 使用后端新版本，冲突不得覆盖用户内容', /version.value = res.stack.version/.test(managedConfigSrc) && /err.status === 409/.test(managedConfigSrc) && /请先复制当前修改/.test(managedConfigSrc))
check('配置: 确认前快照 payload 且异步回执受卸载 guard 保护', managedConfigSrc.indexOf('const payload = managedUpdatePayload') < managedConfigSrc.indexOf('await confirmAction') && /if \(!alive\) return/.test(managedConfigSrc))
check('安全: env 不写浏览器存储且卸载时清空', !/localStorage|sessionStorage/.test(managedConfigSrc) && /content.value = originalContent.value = envContent.value = originalEnv.value = ''/.test(managedConfigSrc))
check('操作: 启停等需明确确认并受在线/busy/进行中保护', /await confirmAction/.test(managedActionsSrc) && /running.value \|\| !props.online \|\| props.busy/.test(managedActionsSrc))
check('操作: 未同步阻止启动重建', /unsynced && \['start', 'recreate'\].includes\(item.action\)/.test(managedActionsSrc))
check('操作: 删除默认保留卷，显式危险确认后才提交删卷选项', /optionDefault: false, danger: true/.test(managedActionsSrc) && /if \(!result.confirmed \|\| !alive\) return/.test(managedActionsSrc) && /deleteStack\(id, name, result.optionChecked\)/.test(managedActionsSrc))

// ───────────────────────── 结果 ─────────────────────────
const total = passed + failures.length
if (failures.length > 0) {
  console.error(`\n✗ 契约测试失败 ${failures.length}/${total}\n`)
  for (const f of failures) console.error(`  - ${f}`)
  process.exit(1)
}
console.log(`✓ 契约测试全部通过（${passed}/${total}）`)
