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
check('listStacks: stacks 被解成数组并全部保留', Array.isArray(list.stacks) && list.stacks.length === 2)
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

// ───────────────────────── 结果 ─────────────────────────
const total = passed + failures.length
if (failures.length > 0) {
  console.error(`\n✗ 契约测试失败 ${failures.length}/${total}\n`)
  for (const f of failures) console.error(`  - ${f}`)
  process.exit(1)
}
console.log(`✓ 契约测试全部通过（${passed}/${total}）`)
