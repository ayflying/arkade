/**
 * 存储卷文件「放弃请求」契约测试
 *
 * ## 这条契约在守什么
 *
 * 卷文件操作在 Agent 侧有并发闸门（同时最多 2 个 helper 容器）。用户打开一个
 * 大文件后切走页面时，如果只是"丢弃响应"而不真正中断请求，服务端会继续跑到
 * 自己那 60 秒超时，槽位一直被占——之后同一台服务器上的文件操作全部返回
 * 「文件操作繁忙（同时最多 2 个）」。
 *
 * 因此这里守三层：
 * 1. `request.ts`：abort 必须与 network 区分，且能被子组件静默识别；
 * 2. `volumeFiles` / `useVolumeFiles`：signal 一路传到底，并在放弃时 abort；
 * 3. 面板与视图：给用户一个「取消」出口（而不是只能靠切页面）。
 */

import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'
import { pathToFileURL } from 'node:url'
import { registerHooks } from 'node:module'

const here = dirname(fileURLToPath(import.meta.url))
const srcDir = join(here, '..', 'src')

/* 把 '@/' 映射到 src/，并补上 TS 源码里省略的 .ts 扩展名 */
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier.startsWith('@/')) {
      return { url: pathToFileURL(join(srcDir, specifier.slice(2) + '.ts')).href, shortCircuit: true }
    }
    // 源码内部用 `./errorMessages` 这种无扩展名写法（打包器能解析），
    // Node 的 ESM 解析器不能，这里补一次。
    if (specifier.startsWith('.') && context.parentURL !== undefined && !/\.[a-z]+$/.test(specifier)) {
      const candidate = new URL(specifier + '.ts', context.parentURL)
      if (candidate.href.startsWith(pathToFileURL(srcDir).href)) {
        return { url: candidate.href, shortCircuit: true }
      }
    }
    return nextResolve(specifier, context)
  }
})

const failures = []

function check(label, condition, detail) {
  if (condition) return
  failures.push(detail === undefined ? label : `${label}：${detail}`)
}

/** 造一个与浏览器 AbortError 同形的异常 */
function abortLike() {
  return Object.assign(new Error('The operation was aborted.'), { name: 'AbortError' })
}

const request = await import('../src/api/request.ts')

/* ───────── 1. 请求层：abort 与 network 必须区分 ───────── */

// fetch 被 abort：应得到 aborted，而不是"无法连接服务器"
globalThis.fetch = async () => {
  throw abortLike()
}
let aborted = null
try {
  await request.httpGet('/agents', { signal: new AbortController().signal })
} catch (err) {
  aborted = err
}
check('abort 应抛 ApiError', aborted !== null && typeof aborted.code === 'string')
check('abort 错误码应为 aborted', aborted?.code === 'aborted', String(aborted?.code))
check('abort 文案不应说无法连接服务器', aborted?.message === '请求已取消', String(aborted?.message))
check('isAborted 应识别 abort', request.isAborted(aborted) === true)

// DOMException 形态同样要识别（真实浏览器走这条）
globalThis.fetch = async () => {
  throw new DOMException('aborted', 'AbortError')
}
let abortedDom = null
try {
  await request.httpGet('/agents')
} catch (err) {
  abortedDom = err
}
check('DOMException 形态的 abort 也要识别', request.isAborted(abortedDom) === true, String(abortedDom?.code))

// 真正的网络故障不能被误判成取消，否则用户永远看不到"连不上"
globalThis.fetch = async () => {
  throw new TypeError('Failed to fetch')
}
let netErr = null
try {
  await request.httpGet('/agents')
} catch (err) {
  netErr = err
}
check('网络故障仍是 network', netErr?.code === 'network', String(netErr?.code))
check('网络故障不算 abort', request.isAborted(netErr) === false)

// 响应头到了但正文被中断（大文件读到一半被放弃）也要算取消
globalThis.fetch = async () => ({
  ok: true,
  status: 200,
  text: async () => {
    throw abortLike()
  }
})
let bodyAborted = null
try {
  await request.httpGet('/agents')
} catch (err) {
  bodyAborted = err
}
check('正文读取中断应算取消', request.isAborted(bodyAborted) === true, String(bodyAborted?.code))

/* ───────── 2. 接口层与 composable：signal 一路到底 ───────── */

const apiSource = readFileSync(join(srcDir, 'api', 'volumeFiles.ts'), 'utf8')
check('readVolumeFile 应接受 signal', /readVolumeFile\([\s\S]{0,120}signal\?: AbortSignal/.test(apiSource))
check('listVolumeFiles 应接受 signal', /listVolumeFiles\([\s\S]{0,120}signal\?: AbortSignal/.test(apiSource))
check('submit 应把 signal 交给 httpPost', /httpPost<VolumeFileResponse>\([\s\S]{0,200}signal === undefined \? \{\} : \{ signal \}/.test(apiSource))

const storeSource = readFileSync(join(srcDir, 'composables', 'useVolumeFiles.ts'), 'utf8')
check('应按读取建立 AbortController', /readAbort = new AbortController\(\)|const controller = new AbortController\(\)/ .test(storeSource))
check('读取应把 signal 传下去', /readVolumeFile\([^)]*controller\.signal\)/.test(storeSource))
check('列目录应把 signal 传下去', /listVolumeFiles\([\s\S]{0,120}controller\.signal/.test(storeSource))
check('作废请求时应真正 abort', /function invalidateFileRequests\(\)[\s\S]{0,200}abortRead\(\)/.test(storeSource))
check('应有 cancelRead 出口', /function cancelRead\(\)/.test(storeSource) && /cancelRead,/.test(storeSource))
check('销毁时应兜底 abort', /onScopeDispose\(\(\) => \{[\s\S]{0,120}abortRead\(\)/.test(storeSource))
check('取消不应写成读取失败', /isAborted\(err\)\) return/.test(storeSource))

/* ───────── 3. 界面：用户必须有「取消」出口 ───────── */

const panelSource = readFileSync(join(srcDir, 'components', 'volume', 'VolumeFilePanel.vue'), 'utf8')
check('面板应声明 cancel 事件', /cancel: \[\]/.test(panelSource))
check('加载态应有取消按钮', /@click="emit\('cancel'\)"/.test(panelSource))
check('取消按钮应只在读取中显示', /v-if="loading"[\s\S]{0,400}emit\('cancel'\)/.test(panelSource))

const boxSource = readFileSync(join(srcDir, 'components', 'volume', 'VolumeFileBox.vue'), 'utf8')
check('容器应透传 cancel', /@cancel="emit\('cancel'\)"/.test(boxSource))

const viewSource = readFileSync(join(srcDir, 'views', 'VolumeFilesView.vue'), 'utf8')
check('视图应把 cancel 接到 cancelRead', /@cancel="store\.cancelRead"/.test(viewSource))
check('卸载时应作废请求', /onUnmounted\(\(\) => \{[\s\S]{0,160}invalidateFileRequests\(\)/.test(viewSource))

// 下载走同一条读取接口、同样占并发槽位，放弃时也必须中断
const downloadSource = readFileSync(join(srcDir, 'composables', 'useVolumeDownload.ts'), 'utf8')
check('下载应持有 AbortController', /let inFlight: AbortController \| null = null/.test(downloadSource))
check('下载应把 signal 传下去', /readVolumeFile\([^)]*controller\.signal\)/.test(downloadSource))
check('作废下载时应真正 abort', /function invalidate\(\): void \{[\s\S]{0,160}inFlight\?\.abort\(\)/.test(downloadSource))
check('下载取消不应弹失败提示', /isAborted\(err\)/.test(downloadSource))

test('存储卷放弃请求契约', () => {
  assert.deepEqual(failures, [], failures.join('\n'))
  console.log(`✓ 存储卷放弃请求契约：全部通过`)
})
