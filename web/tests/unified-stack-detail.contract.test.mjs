import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { registerHooks } from 'node:module'
import { createRequire } from 'node:module'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import ts from 'typescript'
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const source = path => readFileSync(resolve(root, path), 'utf8')
const require = createRequire(import.meta.url)
const model = await import('../src/components/stack/stackDetailModel.ts')
// 网络与容器 IP 拆成两列后，模型也要给出结构化结果而不是拼好的一整串：
// 拼成一串时网络名一长就会把地址挤没，两格才能各自复制。
assert.deepEqual(model.containerNetworkView({}), { kind: 'none' })
assert.deepEqual(model.containerNetworkView({ network_mode: 'host', networks: [{ name: 'host', ip_address: '' }] }), { kind: 'host' })
assert.deepEqual(
  model.containerNetworkView({ networks: [{ name: 'allinssl_default', ip_address: '172.29.0.2' }] }),
  { kind: 'networks', entries: [{ name: 'allinssl_default', ipv4: '172.29.0.2', ipv6: '' }] }
)
// 一个容器加入多个网络：两个字段各自成行，行序与 docker 返回一致
assert.deepEqual(
  model.containerNetworkView({ networks: [
    { name: 'bridge', ip_address: '172.18.0.2', ipv6_address: 'fd00::2' },
    { name: 'private', ip_address: '' }
  ] }).entries.map(e => [e.name, model.networkAddressText(e)]),
  [['bridge', '172.18.0.2'], ['private', '']]
)
// 只有 IPv6 时不能显示空白：地址列退回 IPv6
assert.equal(model.networkAddressText({ name: 'x', ipv4: '', ipv6: 'fd00::9' }), 'fd00::9')
assert.equal(model.sameStackTarget(['a', 'one'], ['a', 'two']), false)
assert.equal(model.sameStackTarget(['a', 'one'], ['b', 'one']), false)
assert.equal(model.sameStackTarget(['a', 'one'], ['a', 'one']), true)
const busy = { status: 'busy', running: 0, total: 0 }
assert.deepEqual(model.stackContainerCounts(busy, [{ state: 'running' }, { state: 'running' }], ''), { running: 2, total: 2 })
assert.deepEqual(model.stackContainerCounts(busy, [], '查询失败'), { running: null, total: null })
assert.deepEqual(model.stackContainerCounts({ ...busy, status: 'stopped' }, [], ''), { running: 0, total: 0 })

// Compile every new SFC, including the real templates (not regex-only source tests).
const components = ['views/StackDetailView.vue', 'components/stack/ManagedStackConfigPanel.vue', 'components/stack/ManagedStackActions.vue', 'components/stack/ExternalStackConfigSection.vue', 'components/stack/StackContainersTable.vue', 'components/stack/StackContainerLogs.vue']
for (const path of components) {
  const filename = resolve(root, 'src', path)
  const { descriptor, errors } = parse(readFileSync(filename, 'utf8'), { filename })
  assert.deepEqual(errors, [])
  const script = compileScript(descriptor, { id: path })
  const template = compileTemplate({ source: descriptor.template.content, filename, id: path, compilerOptions: { bindingMetadata: script.bindings } })
  assert.deepEqual(template.errors, [], path)
  assert.ok(script.content.length > 0)
}

// Execute the real managed payload builder after TypeScript transpilation.
const managed = source('src/composables/useManagedStackConfig.ts')
const js = ts.transpileModule(managed, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext } }).outputText
const stub = `data:text/javascript,${encodeURIComponent('export const getStackConfig=()=>{}; export const updateStack=()=>{}; export class ApiError extends Error {}; export const confirmAction=()=>false; export const toastError=()=>{}; export const toastSuccess=()=>{}; export const toastWarning=()=>{};')}`
const hook = registerHooks({ resolve(specifier, context, next) {
  if (context.parentURL?.startsWith('data:') && specifier.startsWith('@/')) return { url: stub, shortCircuit: true }
  if (context.parentURL?.startsWith('data:') && specifier === 'vue') return { url: new URL(`file:///${require.resolve('vue').replaceAll('\\', '/')}`).href, shortCircuit: true }
  return next(specifier, context)
} })
const { managedUpdatePayload } = await import(`data:text/javascript;base64,${Buffer.from(js).toString('base64')}`)
assert.deepEqual(managedUpdatePayload('yaml', 7, 'SECRET=x', 'SECRET=x'), { content: 'yaml', version: 7 })
assert.deepEqual(managedUpdatePayload('yaml', 7, '', 'SECRET=x'), { content: 'yaml', version: 7, env_content: '' })
assert.deepEqual(managedUpdatePayload('yaml', 7, 'SECRET=y', 'SECRET=x'), { content: 'yaml', version: 7, env_content: 'SECRET=y' })
hook.deregister()
let response, called
const apiStub = `data:text/javascript,${encodeURIComponent('export async function httpGet(url) { globalThis.__UNIFIED_CALL__(url); return globalThis.__UNIFIED_RESPONSE__() }; export const httpPost=()=>{}; export const httpPut=()=>{}; export const httpDelete=()=>{};')}`
globalThis.__UNIFIED_CALL__ = url => { called = url }
globalThis.__UNIFIED_RESPONSE__ = () => response
const apiHook = registerHooks({ resolve(specifier, context, next) {
  if (specifier === './request' && context.parentURL?.includes('/api/stacks.ts')) return { url: apiStub, shortCircuit: true }
  return next(specifier, context)
} })
const api = await import('../src/api/stacks.ts')
response = { stack: { name: 'external', managed: false, auto_adopt_status: 'skipped', auto_adopt_reason: 'build', status: 'running' }, containers: [{ id: 'c1', networks: [{ name: 'private', ip_address: '172.18.0.2' }] }] }
const external = await api.getUnifiedStack('a/b', 'one two')
assert.equal(called, '/agents/a%2Fb/stacks/one%20two')
assert.equal(external.stack.managed, false)
assert.equal(external.stack.auto_adopt_reason, 'build')
assert.equal(external.containers[0].networks[0].ip_address, '172.18.0.2')
response = { stack: { name: 'managed', managed: true, version: 9 }, containers_error: 'offline' }
const owned = await api.getUnifiedStack('a', 'managed')
assert.equal(owned.stack.managed, true)
assert.equal(owned.stack.version, 9)
assert.equal(owned.stack.total, null)
assert.equal(owned.containers_error, 'offline')
assert.deepEqual(owned.containers, [])
apiHook.deregister()

// 用真实 Vue 生命周期验证：进入忙碌详情后自动刷新，恢复后停止，离开也清掉计时器。
const detailHook = registerHooks({ resolve(specifier, context, next) {
  if (context.parentURL?.includes('/composables/useStackDetail.ts')) {
    if (specifier === '@/api/stacks') return { url: new URL('../src/api/stacks.ts', import.meta.url).href, shortCircuit: true }
    if (specifier === '@/components/stack/stackDetailModel') return { url: new URL('../src/components/stack/stackDetailModel.ts', import.meta.url).href, shortCircuit: true }
  }
  return next(specifier, context)
} })
const { useStackDetail } = await import('../src/composables/useStackDetail.ts')
detailHook.deregister()
const { effectScope, ref, nextTick } = await import('vue')
const actualSetTimeout = globalThis.setTimeout, actualClearTimeout = globalThis.clearTimeout
const timers = new Map()
let timerID = 0
const scope = effectScope()
try {
  globalThis.setTimeout = (fn, delay) => {
    assert.equal(delay, 5000)
    timers.set(++timerID, fn)
    return timerID
  }
  globalThis.clearTimeout = id => timers.delete(id)
  const project = ref('managed')
  const settle = () => new Promise(resolve => setImmediate(resolve))
  response = { stack: { name: 'managed', managed: true, status: 'busy' }, containers: [] }
  const detail = scope.run(() => useStackDetail(() => ['a', project.value]))
  await settle()
  assert.equal(detail.stack.value.status, 'busy')
  assert.equal(timers.size, 1)
  const [id, tick] = timers.entries().next().value
  timers.delete(id)
  response = { stack: { name: 'managed', managed: true, status: 'running' }, containers: [] }
  tick()
  await settle()
  assert.equal(detail.stack.value.status, 'running')
  assert.equal(timers.size, 0)
  response = { stack: { name: 'another', managed: true, status: 'busy' }, containers: [] }
  project.value = 'another'
  await nextTick()
  await settle()
  assert.equal(called, '/agents/a/stacks/another')
  assert.equal(timers.size, 1)
  scope.stop()
  assert.equal(timers.size, 0)
} finally {
  scope.stop()
  globalThis.setTimeout = actualSetTimeout
  globalThis.clearTimeout = actualClearTimeout
}
delete globalThis.__UNIFIED_CALL__
delete globalThis.__UNIFIED_RESPONSE__
const view = source('src/views/StackDetailView.vue')
assert.equal((view.match(/<page-header\b/g) ?? []).length, 1)
assert.ok(view.indexOf('name="containers"') < view.indexOf('name="config"'))
assert.ok(view.includes("ref('containers')"))
assert.ok(!view.includes('listStackPs'))
assert.ok(source('src/router/index.ts').includes("redirect: (to) => ({ name: 'agent-stack-detail'"))
console.log('✓ Unified stack detail: network rendering, stale targets, .env payload, six compiled SFCs and canonical shell passed')
