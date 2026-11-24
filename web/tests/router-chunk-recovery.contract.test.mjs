import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'

const read = path => readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
const encode = js => `data:text/javascript;base64,${Buffer.from(js).toString('base64')}`
const transpile = path => ts.transpileModule(read(path), {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 }
}).outputText

/**
 * 加载真实的 chunkLoadRecovery.ts，只把统一提示出口换成假实现。
 * 追加注释让每次导入的 data URL 不同，避免 ESM 模块缓存互相干扰。
 */
async function loadRecovery(tag) {
  const feedback = encode(`
    export const notified = []
    export const toastError = text => { notified.push(text) }
  `)
  const source = transpile('router/chunkLoadRecovery.ts')
    .replace(/(['"])@\/utils\/feedback\1/, JSON.stringify(feedback))
  return { mod: await import(encode(`${source}\n// ${tag}`)), feedback: await import(feedback) }
}

test('chunk load failures are recognised across browsers', async () => {
  const { mod } = await loadRecovery('recognise')
  for (const message of [
    'Failed to fetch dynamically imported module: https://arkade.test/assets/AccountView-abc.js',
    'error loading dynamically imported module',
    'Importing a module script failed.',
    'Unable to preload CSS for /assets/index-abc.css'
  ]) {
    assert.equal(mod.isChunkLoadError(new Error(message)), true, message)
  }
  assert.equal(mod.isChunkLoadError('failed to fetch dynamically imported module'), true)
  // 业务错误绝不能被当成资源问题，否则会把普通报错升级成整页刷新
  assert.equal(mod.isChunkLoadError(new Error('请求失败：500')), false)
  assert.equal(mod.isChunkLoadError(new Error('当前账户无权执行此操作。')), false)
  assert.equal(mod.isChunkLoadError(undefined), false)
})

test('a stale chunk reloads once per target, then asks for a manual refresh', async () => {
  const { mod, feedback } = await loadRecovery('reload')
  let clock = 1_000
  const reloads = []
  const store = new Map()
  const storage = {
    getItem: key => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => { store.set(key, String(value)) }
  }
  const handlers = []
  const router = { onError: (handler) => { handlers.push(handler); return () => { handlers.length = 0 } } }

  const remove = mod.installChunkLoadRecovery(router, {
    reload: url => reloads.push(url),
    now: () => clock,
    storage
  })
  assert.equal(typeof remove, 'function')
  assert.equal(handlers.length, 1)
  const fire = (error, fullPath) => handlers[0](error, { fullPath }, { fullPath: '/overview' })
  const stale = () => new Error('Failed to fetch dynamically imported module')

  // 1. 普通业务错误：不刷新、不提示
  fire(new Error('Request failed with status 500'), '/account')
  assert.deepEqual(reloads, [])
  assert.deepEqual(feedback.notified, [])

  // 2. 第一次拿不到 chunk：整页刷新到用户刚才点的地址
  fire(stale(), '/account')
  assert.deepEqual(reloads, ['/account'])
  assert.match(String(feedback.notified.at(-1)), /正在重新加载页面/)

  // 3. 冷却期内同一目标再次失败：不再刷新（否则会死循环），改为提示手动刷新
  clock += 5_000
  fire(stale(), '/account')
  assert.deepEqual(reloads, ['/account'])
  assert.match(String(feedback.notified.at(-1)), /请手动刷新页面/)

  // 4. 过了冷却期：允许再刷新一次
  clock += mod.RELOAD_GUARD_MS
  fire(stale(), '/account')
  assert.deepEqual(reloads, ['/account', '/account'])

  // 5. 换目标不受上一个目标的记录影响
  clock += 1_000
  fire(stale(), '/agents/agent-1/containers')
  assert.deepEqual(reloads, ['/account', '/account', '/agents/agent-1/containers'])
})

test('storage failures never break the reload path', async () => {
  const { mod } = await loadRecovery('storage')
  const reloads = []
  const handlers = []
  const router = { onError: (handler) => { handlers.push(handler); return () => {} } }
  mod.installChunkLoadRecovery(router, {
    reload: url => reloads.push(url),
    now: () => 1,
    storage: { getItem: () => { throw new Error('denied') }, setItem: () => { throw new Error('denied') } }
  })
  handlers[0](new Error('Failed to fetch dynamically imported module'), { fullPath: '/audit' }, { fullPath: '/' })
  assert.deepEqual(reloads, ['/audit'])
})

test('the router installs the recovery and stays reachable from personal settings', () => {
  const routerEntry = read('router/index.ts')
  assert.match(routerEntry, /import \{ installChunkLoadRecovery \} from '\.\/chunkLoadRecovery'/)
  assert.match(routerEntry, /installChunkLoadRecovery\(router\)/)

  const source = read('router/chunkLoadRecovery.ts')
  assert.match(source, /export const RELOAD_GUARD_KEY = 'arkade:chunk-reload'/)
  assert.match(source, /if \(!isChunkLoadError\(error\)\) return/)
  assert.match(source, /import \{ toastError \} from '@\/utils\/feedback'/)
})
