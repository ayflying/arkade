/**
 * 存储卷文件管理契约测试（纯 Node，无需测试框架）
 *
 * ## 这个测试最该守住的三件事
 *
 * 1. **请求体形状**：`{volume, operation, path, ...}` 逐字对齐定稿，
 *    尤其 write 的 `expected_sha256` 必须真的发出去——
 *    漏发就等于放弃了「防覆盖别人修改」这个保护，而界面上完全看不出来。
 *
 * 2. **二进制判定**：数据库文件必须被判为二进制、拒绝编辑。
 *    判错的后果不是显示难看，而是把数据库写坏且不可恢复。
 *
 * 3. **UTF-8 base64 往返**：中文内容必须原样回来。
 *    用 btoa/atob 直接处理会在第一个中文字符上就抛错或变乱码。
 *
 * 运行：node tests/volume-files.contract.test.mjs
 */

import { registerHooks } from 'node:module'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const fixture = JSON.parse(
  readFileSync(join(here, 'fixtures', 'real-volume-file-responses.json'), 'utf8')
)
const testFilePath = fileURLToPath(import.meta.url)
// 几个源文件在下面多处断言要用，统一在这里读一次
const viewSource = readFileSync(join(here, '..', 'src', 'views', 'VolumeFilesView.vue'), 'utf8')

let nextResponse = null
let lastCall = null

const requestStubSource = `
export async function httpGet(url) { return globalThis.__VOL_STUB__.get(url) }
export async function httpPost(url, body) { return globalThis.__VOL_STUB__.post(url, body) }
export async function httpPut(url, body) { return globalThis.__VOL_STUB__.put(url, body) }
export async function httpDelete(url) { return globalThis.__VOL_STUB__.del(url) }
`

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === './request' && context.parentURL?.includes('/api/volumeFiles.ts')) {
      return { url: 'stub:vol-request', shortCircuit: true }
    }
    return nextResolve(specifier, context)
  },
  load(url, context, nextLoad) {
    if (url === 'stub:vol-request') {
      return { format: 'module', source: requestStubSource, shortCircuit: true }
    }
    return nextLoad(url, context)
  }
})

globalThis.__VOL_STUB__ = {
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

const api = await import('../src/api/volumeFiles.ts')
const b64 = await import('../src/utils/base64.ts')
const bin = await import('../src/utils/binaryDetect.ts')

// ───────────────────────── 路径与基础形状 ─────────────────────────
nextResponse = fixture.list
const listed = await api.listVolumeFiles('ag-1', 'pgdata', 'base')
check('list: 路径正确', lastCall.url === '/agents/ag-1/volume-files', lastCall.url)
check('list: method 是 POST（单 endpoint）', lastCall.method === 'POST')
check(
  'list: 请求体恰好三个字段',
  JSON.stringify(Object.keys(lastCall.body ?? {}).sort()) === '["operation","path","volume"]',
  JSON.stringify(lastCall.body)
)
check('list: operation 是 list', lastCall.body.operation === 'list')
check('list: volume 透传', lastCall.body.volume === 'pgdata')
check('list: path 透传', lastCall.body.path === 'base')
// 对 agentId 做编码，避免 id 含特殊字符时拼坏路径
await api.listVolumeFiles('a b', 'v', '')
check('list: agentId 被 URL 编码', lastCall.url === '/agents/a%20b/volume-files', lastCall.url)

check('list: entries 解出 4 项', listed.entries?.length === 4, String(listed.entries?.length))
for (const [i, entry] of (listed.entries ?? []).entries()) {
  noUndefined(`list: 第 ${String(i)} 项无 undefined 字段`, entry)
}
const [dirEntry, fileEntry, symlinkEntry, unsupportedEntry] = listed.entries ?? []
check('list: 目录 kind 正确', dirEntry.kind === 'directory', String(dirEntry.kind))
check('list: 文件 kind 正确', fileEntry.kind === 'file', String(fileEntry.kind))
check('list: 符号链接 kind 正确', symlinkEntry.kind === 'symlink', String(symlinkEntry.kind))
check('list: 不支持类型 kind 正确', unsupportedEntry.kind === 'unsupported', String(unsupportedEntry.kind))
// 只有 file 可读，只有 directory 可进入
check('list: file 可读', api !== null && fileEntry.kind === 'file')
check('list: directory 不可当文件读', dirEntry.kind !== 'file')
check('list: symlink 不是 file（不可读）', symlinkEntry.kind !== 'file')
check('list: unsupported 不是 file', unsupportedEntry.kind !== 'file')

// ───────────────────────── read ─────────────────────────
nextResponse = fixture.read
const read = await api.readVolumeFile('ag-1', 'pgdata', 'app.conf')
check(
  'read: 请求体只有 operation/path/volume',
  JSON.stringify(Object.keys(lastCall.body ?? {}).sort()) === '["operation","path","volume"]',
  JSON.stringify(lastCall.body)
)
check('read: operation 是 read', lastCall.body.operation === 'read')
// sha256 必须原样返回给调用方——后续 write 要拿它做乐观锁
check('read: 返回 sha256', read.sha256 === fixture.read.result.sha256, String(read.sha256))
check('read: 返回 size', read.size === fixture.read.result.size, String(read.size))
check('read: 返回内容', typeof read.data_base64 === 'string')

// ───────────────────────── write（乐观锁） ─────────────────────────
nextResponse = fixture.write
await api.writeVolumeFile('ag-1', 'pgdata', {
  path: 'app.conf',
  dataBase64: 'aGVsbG8=',
  expectedSha256: 'abc123'
})
check('write: operation 是 write', lastCall.body.operation === 'write')
/*
 * 这条是本组最关键断言之一：
 * expected_sha256 不发出去，后端就无法判断「我读到之后别人有没有改过」，
 * 会静默覆盖他人的修改——而界面上完全看不出少了这个保护。
 */
check(
  'write: 必须带 expected_sha256',
  lastCall.body.expected_sha256 === 'abc123',
  String(lastCall.body.expected_sha256)
)
check('write: data_base64 透传', lastCall.body.data_base64 === 'aGVsbG8=')
check('write: 覆盖写不带 create_only', lastCall.body.create_only === undefined)

// 新建文件：create_only=true 且不带 expected_sha256
nextResponse = fixture.write
await api.writeVolumeFile('ag-1', 'pgdata', {
  path: 'new.conf',
  dataBase64: '',
  createOnly: true
})
check('write: create_only 是 true', lastCall.body.create_only === true)
check('write: 新建时不带 expected_sha256', lastCall.body.expected_sha256 === undefined)

// ───────────────────────── mkdir / rename / delete ─────────────────────────
nextResponse = fixture.mkdir
await api.mkdirVolume('ag-1', 'pgdata', 'newdir')
check('mkdir: operation 是 mkdir', lastCall.body.operation === 'mkdir')
check(
  'mkdir: 请求体只有 operation/path/volume',
  JSON.stringify(Object.keys(lastCall.body ?? {}).sort()) === '["operation","path","volume"]'
)

nextResponse = fixture.rename
await api.renameVolumeEntry('ag-1', 'pgdata', 'old.conf', 'new.conf')
check('rename: operation 是 rename', lastCall.body.operation === 'rename')
check('rename: path 是原路径', lastCall.body.path === 'old.conf')
check('rename: destination 是目标路径', lastCall.body.destination === 'new.conf')
check(
  'rename: 请求体恰好四个字段（不含覆盖开关）',
  JSON.stringify(Object.keys(lastCall.body ?? {}).sort()) === '["destination","operation","path","volume"]',
  JSON.stringify(lastCall.body)
)

nextResponse = fixture.delete
await api.deleteVolumeEntry('ag-1', 'pgdata', 'old.conf')
check('delete: operation 是 delete', lastCall.body.operation === 'delete')
check(
  'delete: 请求体只有 operation/path/volume',
  JSON.stringify(Object.keys(lastCall.body ?? {}).sort()) === '["operation","path","volume"]'
)

// ───────────────────────── base64 UTF-8 往返 ─────────────────────────
/*
 * 中文必须原样往返。用 btoa 直接编码中文会抛 InvalidCharacterError，
 * 用 atob 直接解码会得到乱码——这是这个功能最容易踩的坑，
 * 而卷里的配置文件带中文注释是常态。
 */
const chinese = '数据库密码=测试123\n# 中文注释\n'
const encoded = b64.textToBase64(chinese)
check('base64: 中文编码不抛错', typeof encoded === 'string' && encoded !== '')
check('base64: 中文往返一致', b64.base64ToText(encoded) === chinese, b64.base64ToText(encoded))
check('base64: 编码结果不含非 ASCII', /^[A-Za-z0-9+/=]*$/.test(encoded), encoded)
check('base64: 空串往返', b64.base64ToText(b64.textToBase64('')) === '')

// emoji（4 字节 UTF-8）也要能过
const emoji = '状态：✅ 完成 🎉'
check('base64: emoji 往返一致', b64.base64ToText(b64.textToBase64(emoji)) === emoji)

// 与 Node 自带实现交叉验证，确认不是自己和自己一致
const nodeEncoded = Buffer.from(chinese, 'utf8').toString('base64')
check('base64: 与 Node Buffer 结果一致', encoded === nodeEncoded, `${encoded} vs ${nodeEncoded}`)

// 字节长度计算（用于显示文件大小提示）
check('base64: 字节长度 = 实际 UTF-8 字节数', b64.base64ByteLength(encoded) === Buffer.byteLength(chinese, 'utf8'))
check('base64: 空内容字节长度 0', b64.base64ByteLength('') === 0)

// ───────────────────────── 二进制判定 ─────────────────────────
/*
 * 判定错误方向的代价不对称：
 * - 把文本误判成二进制 → 用户不能编辑（烦，但不会丢数据）；
 * - 把二进制误判成文本 → 用户打开、保存 → **数据损坏且不可恢复**。
 * 所以这里的用例偏向「宁可多判二进制」。
 */

// 纯文本必须判为文本，否则用户没法编辑配置文件
check('binary: 纯 ASCII 文本不是二进制', !bin.looksBinary(new TextEncoder().encode('key=value\n')))
check('binary: 中文 UTF-8 文本不是二进制', !bin.looksBinary(new TextEncoder().encode(chinese)))
check('binary: 空内容不是二进制', !bin.looksBinary(new Uint8Array(0)))

// 含 NUL 的文件（二进制最强信号）必须判为二进制
check('binary: 含 NUL 判为二进制', bin.looksBinary(new Uint8Array([0x41, 0x00, 0x42])))
// 模拟 SQLite 文件头 "SQLite format 3\0"
const sqliteHeader = new TextEncoder().encode('SQLite format 3')
const sqliteBytes = new Uint8Array(sqliteHeader.length + 1)
sqliteBytes.set(sqliteHeader)
sqliteBytes[sqliteHeader.length] = 0
check('binary: SQLite 文件头判为二进制', bin.looksBinary(sqliteBytes))
// 其它不应出现在文本里的控制字符
check('binary: 0x01 判为二进制', bin.looksBinary(new Uint8Array([0x01, 0x41])))
check('binary: 0x7f 判为二进制', bin.looksBinary(new Uint8Array([0x7f, 0x41])))
// 非法 UTF-8 序列
check('binary: 非法 UTF-8 判为二进制', bin.looksBinary(new Uint8Array([0xff, 0xfe, 0x41])))
// 制表/换行/回车/换页是合法文本字符，不能误判
check(
  'binary: Tab/LF/CR/FF 不算二进制',
  !bin.looksBinary(new Uint8Array([0x09, 0x0a, 0x0d, 0x0c, 0x41]))
)

// base64 版本的判定（面板实际走这条路径，因为后端只给 base64）
const binBase64 = Buffer.from([0x53, 0x51, 0x4c, 0x69, 0x74, 0x65, 0x00, 0x41]).toString('base64')
check('binary: base64 含 NUL 判为二进制', bin.looksBinaryBase64(binBase64))
check('binary: base64 文本不判二进制', !bin.looksBinaryBase64(b64.textToBase64(chinese)))
check('binary: base64 空串不判二进制', !bin.looksBinaryBase64(''))

// 换行风格检测（保存时提示用，不做自动转换）
check('binary: 检测 CRLF', bin.detectLineEnding('a\r\nb') === 'crlf')
check('binary: 检测 LF', bin.detectLineEnding('a\nb') === 'lf')

// ───────────────────────── 畸形响应兜底 ─────────────────────────
nextResponse = fixture.degraded.list
const degradedList = await api.listVolumeFiles('ag-1', 'v', '')
check('降级: entries 缺失 → undefined（不当成空目录）', degradedList.entries === undefined)
check('降级: path 兜底空串', degradedList.path === '')

nextResponse = fixture.degraded.entry
const degradedEntryList = await api.listVolumeFiles('ag-1', 'v', '')
const degEntry = degradedEntryList.entries?.[0]
check('降级: 未知 kind 降级为 unsupported', degEntry?.kind === 'unsupported', String(degEntry?.kind))
check('降级: 未知 kind 不退化成 file（不会给编辑入口）', degEntry?.kind !== 'file')
noUndefined('降级: 条目无 undefined 字段', degEntry)

// 响应缺 result：必须报错，不能返回空对象（否则界面显示成空目录）
nextResponse = {}
let threw = false
try {
  await api.listVolumeFiles('ag-1', 'v', '')
} catch {
  threw = true
}
check('降级: 缺 result 抛错而不是返回空对象', threw)

// ───────────────────────── 空文件（data_base64 被 omitempty 省略） ─────────────────────────
/*
 * 空文件是常态（占位配置、.gitkeep、刚建出来的文件）。
 * 后端 `data_base64` 带 omitempty，0 字节文件的 base64 是空串会被直接省略。
 * 若把它当「读不到内容」，空文件就既不能编辑也不能下载——用户会以为文件坏了。
 */
nextResponse = fixture.readEmpty
const readEmpty = await api.readVolumeFile('ag-1', 'v', 'empty.conf')
check(
  '空文件: 响应里确实没有 data_base64（omitempty 已省略）',
  readEmpty.data_base64 === undefined,
  String(readEmpty.data_base64)
)
check('空文件: size 是 0 而不是缺失', readEmpty.size === 0, String(readEmpty.size))
check('空文件: sha256 有值', typeof readEmpty.sha256 === 'string' && readEmpty.sha256 !== '')
// 空内容不是二进制：必须允许展示与编辑
check('空文件: 空串不判为二进制', !bin.looksBinaryBase64(''))
check('空文件: 解码得到空文本', b64.base64ToText('') === '')
check('空文件: 空文本编码为空 base64', b64.textToBase64('') === '')

// 面板里的���省逻辑：undefined → 空串
const panelSrc = readFileSync(
  join(here, '..', 'src', 'components', 'volume', 'VolumeFilePanel.vue'),
  'utf8'
)
check(
  '空文件: 面板把缺失的 data_base64 兜底成空串',
  /props\.result\?\.data_base64 \?\? ''/.test(panelSrc),
  '没有 ?? \'\' 兜底，空文件会打不开'
)

// ───────────────────────── 下载范围 ─────────────────────────
/*
 * 文本也必须能下载：这是文件管理器，用户需要把文件拿走，
 * 不能只给二进制。唯一例外是超限（后端 read 已拒绝，手里没有内容）。
 */
check(
  '下载: 条件是「有结果且未超限」，不再限定二进制',
  /canDownload\s*=\s*computed\(\(\)\s*=>\s*props\.result !== null && !isTooLarge\.value\)/.test(panelSrc),
  '下载条件不对'
)
check('下载: 模板用 canDownload 而不是 isBinary', /v-if="canDownload"/.test(panelSrc))
check('下载: 不再有 isBinary && 下载 的旧条件', !/isBinary &&[^\n]*download/.test(panelSrc))

// ───────────────────────── 文件读取竞态（readSeq） ─────────────────────────
/*
 * 这是本轮最关键的一条。
 *
 * 用户点 A 再点 B，A 的响应如果后到，会把面板刷成 A 的内容而路径显示 B。
 * 此时用户看着 B 的路径编辑 A 的内容，一保存就**写错文件**。
 * listSeq 早已有，但文件读取是另一条流，必须有自己的序号。
 */
const storeSrc = readFileSync(join(here, '..', 'src', 'composables', 'useVolumeFiles.ts'), 'utf8')
check('竞态: 存在独立 readSeq（不与 listSeq 混用）', /let readSeq = 0/.test(storeSrc))
check(
  '竞态: readFile 进入时自增 readSeq 并快照',
  /const seq = \+\+readSeq/.test(storeSrc),
  'readFile 没有自增序号'
)
check(
  '竞态: readFile 成功路径校验 seq !== readSeq 后丢弃',
  /if \(seq !== readSeq\) return/.test(storeSrc),
  '过期成功响应没有被丢弃'
)
// 错误路径也要丢弃，否则 A 的报错会挂到 B 上
const readFileBody = storeSrc.slice(storeSrc.indexOf('async function readFile'))
check(
  '竞态: readFile 的错误路径同样丢弃过期响应',
  /catch[\s\S]{0,200}if \(seq !== readSeq\) return/.test(readFileBody.slice(0, 1500)),
  '过期错误响应没有被丢弃'
)
check(
  '竞态: finally 只在最新请求时清 loading',
  /if \(seq === readSeq\) fileLoading\.value = false/.test(storeSrc),
  '旧请求会提前清掉 loading，导致最新请求看起来已结束'
)
check(
  '竞态: 提供 invalidateFileRequests 供路由变化/卸载调用',
  /function invalidateFileRequests/.test(storeSrc)
)
check(
  '竞态: closeFile 先作废再清空',
  /invalidateFileRequests\(\)[\s\S]{0,200}fileResult\.value = null/.test(storeSrc),
  'closeFile 没作废请求，迟到的 read 会把面板填回来'
)

// 保存：必须快照路径与令牌，不能 await 之后重读 openFilePath
check(
  '竞态: saveFile 进入时快照目标路径',
  /const targetPath = openFilePath\.value/.test(storeSrc),
  'saveFile 没有快照路径'
)
check(
  '竞态: saveFile 快照读取序号',
  /const savedSeq = readSeq/.test(storeSrc),
  'saveFile 没有快照 readSeq'
)
check(
  '竞态: 保存结果只在路径与序号都没变时才写回面板',
  /readSeq === savedSeq && openFilePath\.value === targetPath/.test(storeSrc),
  '缺少双重校验，A 的保存结果会标到 B 上'
)
// 写入必须用快照路径，不能重读响应式状态
check(
  '竞态: 写入用快照路径而不是重读 openFilePath',
  /path: targetPath/.test(storeSrc),
  '写入路径用了重读值，导航后可能写错文件'
)
check(
  '竞态: saveFile 返回路径而不是布尔',
  /Promise<string \| null>/.test(storeSrc),
  '返回布尔的话视图无法校验结果属于哪个文件'
)

// 视图层：保存后校验路径仍指向当前文件
check(
  '竞态: 视图在 markSaved 前校验 savedPath 仍是当前文件',
  /savedPath !== store\.openFilePath\.value/.test(viewSource),
  '视图没校验，会把旧文件保存结果标到新文件上'
)

// 卸载清理
check(
  'unmount: 视图在卸载时作废文件请求',
  /onUnmounted[\s\S]{0,300}invalidateFileRequests/.test(viewSource),
  '卸载时没有作废请求'
)

// ───────────────────────── 硬刷新（beforeunload） ─────────────────────────
/*
 * 路由守卫拦不住硬刷新/关标签页——整页卸载走浏览器自己的流程，
 * Vue Router 压根不参与。唯一可用的钩子是 beforeunload，
 * 且浏览器只弹它自己的通用确认框、文案不可定制。
 * 守卫与 beforeunload 都收在 useUnsavedGuard 里。
 */
const guardSrc = readFileSync(
  join(here, '..', 'src', 'composables', 'useUnsavedGuard.ts'),
  'utf8'
)

check(
  '硬刷新: 注册 beforeunload 监听',
  /addEventListener\('beforeunload'/.test(guardSrc)
)
check(
  '硬刷新: 有未保存改动时 preventDefault',
  /isDirty\(\)[\s\S]{0,120}event\.preventDefault\(\)/.test(guardSrc),
  '没有 preventDefault，浏览器不会拦'
)
check(
  '硬刷新: 同时设置 returnValue（老浏览器只认它）',
  /event\.returnValue = ''/.test(guardSrc)
)
check(
  '硬刷新: 卸载时移除监听（避免路由复用时重复注册）',
  /removeEventListener\('beforeunload'/.test(guardSrc)
)
// 页面必须真的用上这个守卫，不能只是写了个文件没人调
check('硬刷新: 视图实际调用 useUnsavedGuard', /useUnsavedGuard\(/.test(viewSource))

// ───────────────────────── 视图结构约束（防回归） ─────────────────────────
/*
 * 这些断言不是「风格偏好」，每一条都对应一种会真正丢数据或误导用户的退化：
 *
 * 1. 未保存确认必须挂在**路由守卫**上。只在按钮上写 confirmLeaveDirty 的话，
 *    浏览器后退、点侧边栏切页、手改地址栏全都绕过去了——用户以为存了其实没存。
 * 2. 主 view 必须保持可读长度，否则守卫/编排/模板混在一起，改一处容易碰坏另一处。
 * 3. 超限文件不能给下载按钮：后端 read 已拒绝，没有内容可下载。
 */
check(
  'view: 用 onBeforeRouteLeave 拦后退/侧边栏/地址栏',
  guardSrc.includes('onBeforeRouteLeave')
)
check(
  'view: 用 onBeforeRouteUpdate 拦同路由换参数（切目录/切卷）',
  guardSrc.includes('onBeforeRouteUpdate')
)
// 返回 false 才会真正取消导航；只 return true / 不返回相当于没拦
check(
  'view: update 守卫在用户取消时 return false',
  /onBeforeRouteUpdate[\s\S]{0,400}return false/.test(guardSrc),
  '守卫里没找到 return false，取消导航会失效'
)
// 守卫必须真的问「有没有未保存改动」，而不是永远放行
check(
  'view: 守卫调用 confirmLeaveDirty',
  /onBeforeRouteLeave[\s\S]{0,200}confirmLeaveDirty/.test(guardSrc),
  'leave 守卫没有调用 confirmLeaveDirty'
)

// 去掉末尾空行再数：文件末尾的换行不算一行
const viewLineCount = viewSource.replace(/\n$/, '').split('\n').length
check(
  'view: 主文件不超过 300 行',
  viewLineCount <= 300,
  `实际 ${String(viewLineCount)} 行`
)

// 超限提示里不能出现下载引导
check(
  'panel: 下载按钮不再对超限文件开放',
  !/isBinary \|\| isTooLarge/.test(panelSrc),
  '下载按钮仍然是 (isBinary || isTooLarge)，超限会给出必然失败的按钮'
)
check(
  'panel: 超限文案说明当前限制 4 MiB',
  /当前限制/.test(panelSrc)
)

// ───────────────────────── 新 SFC 的 Naive 组件 import 完整性 ─────────────────────────
/*
 * ⚠️ 这条是真实事故的防回归，不是风格检查。
 *
 * 项目**没有全局注册** Naive UI 组件，模板里的每个 `<n-xxx>` 都必须在
 * `<script setup>` 里显式 import。而 vue-tsc 对**未注册的 kebab 标签不报错**，
 * 所以漏一个的效果是：编译通过、类型通过、控制台无错误，但那个组件整个不渲染。
 *
 * 本次事故：VolumeEntryTable 用了 `<n-data-table>` 却没 import NDataTable，
 * 结果「列表里有 1 条数据但表格一行都没有」，极难从日志发现。
 */
const NAIVE_SFCS = [
  'views/VolumeFilesView.vue',
  'components/volume/VolumeBreadcrumb.vue',
  'components/volume/VolumeToolbar.vue',
  'components/volume/VolumeEntryTable.vue',
  'components/volume/VolumeFileBox.vue',
  'components/volume/VolumeFilePanel.vue',
  'components/volume/VolumeCreateDialog.vue',
  'components/volume/VolumeRenameDialog.vue'
]

/** kebab 标签名 → PascalCase 组件名（n-data-table → NDataTable） */
function toPascal(tag) {
  return tag
    .split('-')
    .filter((s) => s !== '')
    .map((s) => s.charAt(0).toUpperCase() + s.slice(1))
    .join('')
}

for (const rel of NAIVE_SFCS) {
  const src = readFileSync(join(here, '..', 'src', rel), 'utf8')
  // 模板部分里所有 <n-xxx 开头的标签
  const usedTags = [...new Set([...src.matchAll(/<(n-[a-z-]+)/g)].map((m) => m[1]))]
  // script 中来自 naive-ui 的具名 import
  const naiveImports = [...src.matchAll(/import\s*\{([^}]*)\}\s*from\s*['"]naive-ui['"]/g)]
    .map((m) => m[1])
    .join(',')
  const missing = usedTags.filter((tag) => !new RegExp(`\\b${toPascal(tag)}\\b`).test(naiveImports))
  const name = rel.split('/').pop()
  check(
    `${name}: 模板用到的 Naive 组件都已 import`,
    missing.length === 0,
    missing.length === 0 ? '' : `缺少 ${missing.join(', ')}`
  )
}

// 事故点单独钉死：VolumeEntryTable 必须有 NDataTable
const entryTableSrc = readFileSync(
  join(here, '..', 'src', 'components', 'volume', 'VolumeEntryTable.vue'),
  'utf8'
)
check(
  'VolumeEntryTable: 使用 <n-data-table> 且已 import NDataTable',
  /<n-data-table/.test(entryTableSrc) && /\bNDataTable\b/.test(entryTableSrc),
  '模板用了 n-data-table 但没 import NDataTable，表格会整个不渲染'
)

// ───────────────────────── 上传（create_only） ─────────────────────────
/*
 * 上传的核心风险：没有 expected_sha256 可比对，一旦允许覆盖就是
 * 用本地文件**无条件冲掉**服务器上的现有文件。所以必须 create_only。
 *
 * 另一条：大小检查要在读内容**之前**。先 arrayBuffer 再判断的话，
 * 用户选个 2 GB 的文件会先把内存打满才被拒绝。
 */
/*
 * 只取函数体并**剥掉注释**再判断：
 * 函数自己的 JSDoc 里也写了 `file.arrayBuffer()` 和 `createOnly` 之类的字面量，
 * 不剥注释会把说明文字误当成真实调用（之前就是这样误判位置）。
 */
const uploadFnIdx = storeSrc.indexOf('async function uploadCreate')
/** 从函数开始到下一个顶层 `/** 分节注释` 之前，粗略取出该函数源码 */
const uploadRawEnd = storeSrc.indexOf('/** 重命名 / 移动', uploadFnIdx)
const uploadRaw = storeSrc.slice(uploadFnIdx, uploadRawEnd > 0 ? uploadRawEnd : storeSrc.length)
const uploadBodySig = uploadRaw
  .replace(/\/\*[\s\S]*?\*\//g, '') // 去块注释
  .replace(/\/\/[^\n]*/g, '') // 去行注释

check(
  '上传: 使用 create_only，绝不覆盖同名文件',
  /createOnly: true/.test(uploadBodySig),
  '上传没有 createOnly，会覆盖服务器上的同名文件'
)
check(
  '上传: 不带 expected_sha256（上传没有可比对的旧内容）',
  !/expectedSha256/.test(uploadBodySig)
)
/*
 * 大小检查必须在 arrayBuffer 之前。
 * 定位用「实际比较语句」而不是常量首次出现——常量在注释里也会出现，
 * 按首次出现会误判位置。
 */
const sizeCheckIdx = uploadBodySig.search(/file\.size\s*>\s*VOLUME_FILE_MAX_BYTES/)
const arrayBufferIdx = uploadBodySig.indexOf('arrayBuffer')
check(
  '上传: 先按 file.size 拦截，再读内容',
  sizeCheckIdx > -1 && arrayBufferIdx > -1 && sizeCheckIdx < arrayBufferIdx,
  `大小检查位置 ${String(sizeCheckIdx)}，arrayBuffer 位置 ${String(arrayBufferIdx)}`
)
check(
  '上传: 0 字节允许（后端支持空文件，用户也能下载空的）',
  !/file\.size === 0/.test(uploadBodySig),
  '不应拒绝 0 字节文件'
)
// 二进制不能经过文本解码：只能 bytesToBase64，不能 base64ToText/textToBase64
check(
  '上传: 用 bytesToBase64 原样编码，不做文本转码',
  /bytesToBase64\(/.test(uploadBodySig) && !/textToBase64/.test(uploadBodySig),
  '上传走了文本编码路径，二进制会被损坏'
)

// 工具条：必须有隐藏 file input 与上传按钮，且 input 不能用 display:none
const toolbarSrc = readFileSync(
  join(here, '..', 'src', 'components', 'volume', 'VolumeToolbar.vue'),
  'utf8'
)
check('工具条: 有 <input type="file">', /type="file"/.test(toolbarSrc))
check('工具条: 有上传按钮', /上传文件/.test(toolbarSrc))
check(
  '工具条: 上传期间禁用并 loading（防重复提交）',
  /uploading/.test(toolbarSrc) && /:loading="uploading"/.test(toolbarSrc)
)
check(
  '工具条: 选中后清空 input.value（允许重试同一文件）',
  /input\.value = ''/.test(toolbarSrc),
  '不清空的话，失败后重选同一个文件不会触发 change'
)
// display:none 的 input 有些浏览器不响应 .click()
check(
  '工具条: 隐藏用 0 尺寸而非 display:none',
  !/display:\s*none/.test(toolbarSrc),
  'display:none 会导致部分浏览器 .click() 无效'
)
// 视图必须真的接上上传
check('视图: 接上 handleUpload', /@upload="handleUpload"/.test(viewSource))
check('视图: 用 busy 状态防重入', /if \(uploading\.value\) return/.test(viewSource))

/*
 * 上下文快照：await arrayBuffer 期间用户可能切卷/切目录。
 * 若等 await 回来再读 volume.value / currentPath.value，就写进了另一个卷——
 * 文件出现在用户没预期的位置，还会因为 create_only 在那卷凭空多出一个文件。
 */
check(
  '上传: 进函数就快照 agentId/volume/path',
  /const targetAgentId = agentId\.value/.test(uploadBodySig) &&
    /const targetVolume = volume\.value/.test(uploadBodySig) &&
    /const targetPath = joinPath\(currentPath\.value, file\.name\)/.test(uploadBodySig),
  '缺少上下文快照，await 期间切卷会写错目标'
)
check(
  '上传: 写入用快照值而不是重读响应式状态',
  /writeVolumeFile\(targetAgentId, targetVolume/.test(uploadBodySig) &&
    /path: targetPath/.test(uploadBodySig),
  '写入用了重读值'
)
check(
  '上传: 快照在 arrayBuffer 之前',
  uploadBodySig.search(/const targetAgentId/) < uploadBodySig.search(/arrayBuffer\(\)/),
  '快照位置在 await 之后，已失去意义'
)

// ───────────────────────── 汇总 ─────────────────────────
console.log(`\n${failures.length === 0 ? '✓' : '✗'} 存储卷文件契约测试：${String(passed)} 通过，${String(failures.length)} 失败\n`)
if (failures.length > 0) {
  for (const f of failures) console.log(`  ✗ ${f}`)
  process.exitCode = 1
} else {
  console.log(`✓ 全部通过（${String(passed)}/${String(passed)}）`)
}
