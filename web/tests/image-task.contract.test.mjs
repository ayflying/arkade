/**
 * 镜像任务契约测试（纯 Node，无需测试框架）
 *
 * ## 为什么单独一个文件
 *
 * contract.test.mjs 的模块拦截只对 `api/stacks.ts` 生效（按 parentURL 判断）。
 * 这里要测 `api/imageTasks.ts`，同样需要拦 `./request`，
 * 但拦截规则不同——分开写比在一个文件里堆两套钩子清楚，
 * 也避免两个测试互相干扰（任何一个先跑改了全局 stub 都会串味）。
 *
 * ## 断言重点
 *
 * 1. **路径与请求体逐字对齐 Lead 定稿的 API**（不多一个字段、不少一个）；
 * 2. **`progress` / `bytes_total` 的 null 语义**：null = 未知，绝不能被兜底成 0；
 * 3. **202 是「已接受」不是「已完成」**：拿到 task 不能当成功；
 * 4. 下载地址是普通 API 路径（走 Cookie），不是预签名 URL；
 * 5. 畸形响应不炸。
 *
 * 运行：node tests/image-task.contract.test.mjs
 */

import { registerHooks } from 'node:module'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const fixture = JSON.parse(
  readFileSync(join(here, 'fixtures', 'real-image-task-responses.json'), 'utf8')
)

let nextResponse = null
let lastCall = null

/**
 * 假 request 模块源码。
 *
 * 注意必须提供 imageTasks.ts 实际 import 的**全部**函数：
 * 少一个会导致 ESM 链接期就报错（不是运行时才报），测试会直接崩。
 */
const requestStubSource = `
export async function httpGet(url) { return globalThis.__IMG_STUB__.get(url) }
export async function httpPost(url, body) { return globalThis.__IMG_STUB__.post(url, body) }
export async function httpPut(url, body) { return globalThis.__IMG_STUB__.put(url, body) }
export async function httpDelete(url) { return globalThis.__IMG_STUB__.del(url) }
`

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === './request' && context.parentURL?.includes('/api/imageTasks.ts')) {
      return { url: 'stub:img-request', shortCircuit: true }
    }
    return nextResolve(specifier, context)
  },
  load(url, context, nextLoad) {
    if (url === 'stub:img-request') {
      return { format: 'module', source: requestStubSource, shortCircuit: true }
    }
    return nextLoad(url, context)
  }
})

globalThis.__IMG_STUB__ = {
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
  check(label, bad.length === 0, bad.length === 0 ? '' : `这些字段是 undefined: ${bad.join(', ')}`)
}

const api = await import('../src/api/imageTasks.ts')

// ───────────────────────── pull ─────────────────────────
nextResponse = fixture.pull
const pulled = await api.pullImage('ag-1', { reference: 'nginx:1.27' })
check('pull: 路径与契约一致', lastCall.url === '/agents/ag-1/images/pull', lastCall.url)
check('pull: method 是 POST', lastCall.method === 'POST')
// 请求体只能有 reference 一个键——多传字段会被后端忽略，但说明前端在自造契约
check(
  'pull: 请求体只有 reference',
  JSON.stringify(Object.keys(lastCall.body ?? {})) === '["reference"]',
  JSON.stringify(lastCall.body)
)
check('pull: reference 原样透传', lastCall.body.reference === 'nginx:1.27', String(lastCall.body.reference))
// 202 只代表已接受，task 是 pending/running 而不是成功
check('pull: 返回的 task 不是已成功状态', pulled.task.status !== 'succeeded', String(pulled.task.status))
check('pull: kind 是 pull', pulled.task.kind === 'pull', String(pulled.task.kind))
check('pull: 任务 id 已解析', pulled.task.id === 'task-pull-1', String(pulled.task.id))
check('pull: phase 原样透传（直接展示，不翻译）', pulled.task.phase === '下载层 2/5', String(pulled.task.phase))
check('pull: progress 是真数字', pulled.task.progress === 0.4, String(pulled.task.progress))
noUndefined('pull: 无 undefined 字段', pulled.task)

// ───────────────────────── progress = null 语义 ─────────────────────────
nextResponse = fixture.taskNullProgress
const nullProgress = await api.getImageTask('task-null')
/*
 * 这是本组测试最重要的一条：
 * progress 为 null 表示「无法得知进度」，界面显示「进度未知」。
 * 如果前端把它兜成 0，界面就会显示 0%——那是另一个含义（一点都没动），
 * 会让用户以为卡死并去重启任务。
 */
check(
  'null 进度: progress 保持 null，不被兜成 0',
  nullProgress.progress === null,
  String(nullProgress.progress)
)
check(
  'null 进度: bytes_total 保持 null，不被兜成 0',
  nullProgress.bytes_total === null,
  String(nullProgress.bytes_total)
)
check('null 进度: bytes_done 保留真实数字', nullProgress.bytes_done === 1048576, String(nullProgress.bytes_done))

// ───────────────────────── 导出 / 下载地址 ─────────────────────────
nextResponse = fixture.exportAccepted
const exported = await api.exportImage('ag-1', { reference: 'redis:7' })
check('export: 路径正确', lastCall.url === '/agents/ag-1/images/export', lastCall.url)
check('export: 请求体只有 reference', JSON.stringify(Object.keys(lastCall.body ?? {})) === '["reference"]')
check('export: 刚提交时 download_ready=false', exported.task.download_ready === false)
check('export: kind 是 export', exported.task.kind === 'export', String(exported.task.kind))

nextResponse = fixture.exportDone
const exportDone = await api.getImageTask('task-export-1')
check('export: 完成后 download_ready=true', exportDone.download_ready === true)

/*
 * 下载地址必须是普通 API 路径（由浏览器带 Cookie 请求），
 * 不能是预签名 URL——预签名 URL 会把鉴权信息放进 URL，
 * 且和我们「下载时重新校验权限」的要求冲突。
 */
const downloadUrl = api.imageDownloadUrl('task-export-1')
check(
  'download: 是普通 API 路径',
  downloadUrl === '/api/image-tasks/task-export-1/download',
  downloadUrl
)
check('download: 不是预签名 URL', !downloadUrl.includes('signature') && !downloadUrl.includes('token'))
check('download: 对 id 做了编码', api.imageDownloadUrl('a/b c') === '/api/image-tasks/a%2Fb%20c/download', api.imageDownloadUrl('a/b c'))

// ───────────────────────── 迁移 ─────────────────────────
nextResponse = fixture.migrate
const migrated = await api.migrateImage('ag-1', {
  reference: 'redis:7',
  target_agent_id: 'ag-2',
  confirmation: true
})
check('migrate: 路径正确', lastCall.url === '/agents/ag-1/images/migrate', lastCall.url)
check(
  'migrate: 请求体恰好三个字段',
  JSON.stringify(Object.keys(lastCall.body ?? {}).sort()) === '["confirmation","reference","target_agent_id"]',
  JSON.stringify(lastCall.body)
)
check('migrate: confirmation 必须是 true', lastCall.body.confirmation === true)
// 迁移不是移动：源不能被删
check('migrate: target_agent_id 透传', lastCall.body.target_agent_id === 'ag-2')
check('migrate: 返回目标是 ag-2', migrated.task.target_agent_id === 'ag-2', String(migrated.task.target_agent_id))
check('migrate: kind 是 migrate', migrated.task.kind === 'migrate', String(migrated.task.kind))

// ───────────────────────── 任务列表与取消 ─────────────────────────
nextResponse = fixture.list
const list = await api.listImageTasks('ag-1')
check('list: 路径带 agent_id 查询参数', lastCall.url === '/image-tasks?agent_id=ag-1', lastCall.url)
check('list: 解析出数组', Array.isArray(list) && list.length === 2, String(list.length))
// 列表里每个元素都要能直接渲染，不能有 undefined
for (const [i, task] of list.entries()) {
  noUndefined(`list: 第 ${String(i)} 项无 undefined 字段`, task)
}
check(
  'list: 对 agent_id 做了 URL 编码',
  (await (async () => {
    nextResponse = fixture.list
    await api.listImageTasks('a b')
    return lastCall.url
  })()) === '/image-tasks?agent_id=a%20b'
)

nextResponse = fixture.canceled
const canceled = await api.cancelImageTask('task-pull-1')
check('cancel: 路径正确', lastCall.url === '/image-tasks/task-pull-1/cancel', lastCall.url)
check('cancel: method 是 POST', lastCall.method === 'POST')
check('cancel: 状态变为 canceled', canceled.status === 'canceled', String(canceled.status))

// ───────────────────────── 畸形响应兜底 ─────────────────────────
nextResponse = fixture.degraded.task
const degraded = await api.getImageTask('task-x')
check('降级: id 兜底空串', degraded.id === '', String(degraded.id))
check('降级: phase 兜底空串', degraded.phase === '')
check('降级: message 兜底空串', degraded.message === '')
// 缺 progress 字段也是「未知」，不是 0
check('降级: 缺 progress → null（未知，不是 0）', degraded.progress === null, String(degraded.progress))
check('降级: 缺 bytes_total → null（未知，不是 0）', degraded.bytes_total === null, String(degraded.bytes_total))
check('降级: bytes_done 兜底 0', degraded.bytes_done === 0)
check('降级: download_ready 兜底 false', degraded.download_ready === false)
noUndefined('降级: 无 undefined 字段', degraded)

nextResponse = fixture.degraded.list
const degradedList = await api.listImageTasks('ag-1')
check('降级: tasks 缺失 → 空数组', Array.isArray(degradedList) && degradedList.length === 0)

// ───────────────────────── import 上传（XHR） ─────────────────────────
/*
 * Node 没有 XMLHttpRequest，所以这里装一个最小桩。
 * 只为验证「实际发出的请求头/请求体是什么」，不模拟网络。
 *
 * 为什么要测这一组：导入走的是**独立于 JSON 通道**的 XHR 路径，
 * JSON 契约测试完全覆盖不到它。而 X-Archive-Size 这个头一旦漏发，
 * Hub 在 chunked 上传时拿不到总长度，2 GiB 上限保护和 fallback 判断都会失效，
 * 且这种失效在界面上完全看不出来（上传照样成功）。
 */
let capturedXhr = null

class FakeXhr {
  constructor() {
    this.headers = {}
    this.upload = {}
    this.body = null
    this.status = 202
    this.responseText = JSON.stringify(fixture.migrate)
    this.withCredentials = false
    capturedXhr = this
  }

  open(method, url) {
    this.method = method
    this.url = url
  }

  setRequestHeader(name, value) {
    this.headers[name] = value
  }

  send(body) {
    this.body = body
    // 同步回调 onload，避免测试等待
    if (typeof this.onload === 'function') this.onload()
  }

  abort() {
    if (typeof this.onabort === 'function') this.onabort()
  }

  addEventListener() {}
  removeEventListener() {}
}

const originalXhr = globalThis.XMLHttpRequest
globalThis.XMLHttpRequest = FakeXhr

const fakeFile = { size: 314572800, name: 'redis-7.tar' }
nextResponse = fixture.importAccepted
const imported = await api.importImage('ag-1', fakeFile)

check('import: method 是 POST', capturedXhr.method === 'POST', String(capturedXhr.method))
check('import: 路径正确', capturedXhr.url === '/api/agents/ag-1/images/import', capturedXhr.url)
check(
  'import: Content-Type 是 application/x-tar（不是 multipart）',
  capturedXhr.headers['Content-Type'] === 'application/x-tar',
  String(capturedXhr.headers['Content-Type'])
)
// 核心断言：必须声明确切字节数
check(
  'import: 带 X-Archive-Size 且等于 File.size',
  capturedXhr.headers['X-Archive-Size'] === '314572800',
  String(capturedXhr.headers['X-Archive-Size'])
)
check(
  'import: X-Archive-Size 是十进制字符串（不是科学计数法/带单位）',
  /^\d+$/.test(capturedXhr.headers['X-Archive-Size'] ?? ''),
  String(capturedXhr.headers['X-Archive-Size'])
)
check('import: 没有误用 multipart', capturedXhr.headers['Content-Type'] !== 'multipart/form-data')
// 请求体就是 File 本身，没有被读成字符串/FormData
check('import: body 是原 File 对象（未读入内存）', capturedXhr.body === fakeFile)
check('import: 带 Cookie 凭证', capturedXhr.withCredentials === true)
check('import: 解析出 task', imported.task.id === 'task-migrate-1', String(imported.task.id))

// 小文件也要发这个头，不能只在「大文件」时才加
const tinyFile = { size: 0, name: 'empty.tar' }
await api.importImage('ag-1', tinyFile)
check(
  'import: 0 字节文件也发 X-Archive-Size=0（交给后端判空）',
  capturedXhr.headers['X-Archive-Size'] === '0',
  String(capturedXhr.headers['X-Archive-Size'])
)

globalThis.XMLHttpRequest = originalXhr

// ───────────────────────── 轮询不受全局自动刷新开关控制 ─────────────────────────
/*
 * 这条是防回归用的。
 *
 * 「自动刷新」开关的用户意图是「别一直刷列表」，不是
 * 「我提交的镜像导入不用告诉我进度」。
 * 但项目里的 usePolling 会**强制**叠加 `uiSettings.autoRefresh`——
 * 一旦有人把 useImageTasks 改成复用 usePolling，任务进度就会在
 * 用户关掉自动刷新后静默停止更新，且界面上看不出原因（进度条只是不动了）。
 *
 * 这里直接读源码断言：它既没有 import usePolling，也没有读 uiSettings。
 * 用「不依赖某个模块」来断言，比跑 timer 更稳（不需要假时钟）。
 */
const imageTasksSource = readFileSync(join(here, '..', 'src', 'composables', 'useImageTasks.ts'), 'utf8')
// 只检查真实 import 语句，不检查注释（注释里提到 usePolling 是解释原因）
const importLines = imageTasksSource
  .split('\n')
  .filter((line) => /^\s*import\s/.test(line))
  .join('\n')

check(
  '轮询: 没有 import usePolling（否则会被全局 autoRefresh 关掉）',
  !/from\s+['"].*usePolling['"]/.test(importLines),
  'useImageTasks.ts 里出现了 usePolling 的 import'
)
check(
  '轮询: 没有 import uiSettings（不读全局自动刷新开关）',
  !/from\s+['"].*uiSettings['"]/.test(importLines),
  'useImageTasks.ts 里出现了 uiSettings 的 import'
)
// 自管定时器是「不受全局开关影响」的实现前提
check(
  '轮询: 用自管 setTimeout 调度',
  /setTimeout\s*\(/.test(imageTasksSource)
)

// ───────────────────────── 汇总 ─────────────────────────
console.log(`\n${failures.length === 0 ? '✓' : '✗'} 镜像任务契约测试：${String(passed)} 通过，${String(failures.length)} 失败\n`)
if (failures.length > 0) {
  for (const f of failures) console.log(`  ✗ ${f}`)
  process.exitCode = 1
} else {
  console.log(`✓ 全部通过（${String(passed)}/${String(passed)}）`)
}
