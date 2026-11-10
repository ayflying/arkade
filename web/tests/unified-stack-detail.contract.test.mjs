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
assert.deepEqual(model.containerNetworkLines({}), ['—'])
assert.deepEqual(model.containerNetworkLines({ network_mode: 'host', networks: [{ name: 'host', ip_address: '' }] }), ['主机网络'])
assert.deepEqual(model.containerNetworkLines({ networks: [{ name: 'bridge', ip_address: '172.18.0.2', ipv6_address: 'fd00::2' }] }), ['bridge / 172.18.0.2 / fd00::2'])
assert.deepEqual(model.containerNetworkLines({ networks: [{ name: 'private', ip_address: '' }] }), ['private / —'])
assert.equal(model.sameStackTarget(['a', 'one'], ['a', 'two']), false)
assert.equal(model.sameStackTarget(['a', 'one'], ['b', 'one']), false)
assert.equal(model.sameStackTarget(['a', 'one'], ['a', 'one']), true)

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
delete globalThis.__UNIFIED_CALL__
delete globalThis.__UNIFIED_RESPONSE__
const view = source('src/views/StackDetailView.vue')
assert.equal((view.match(/<page-header\b/g) ?? []).length, 1)
assert.ok(view.indexOf('name="containers"') < view.indexOf('name="config"'))
assert.ok(view.includes("ref('containers')"))
assert.ok(!view.includes('listStackPs'))
assert.ok(source('src/router/index.ts').includes("redirect: (to) => ({ name: 'agent-stack-detail'"))
console.log('✓ Unified stack detail: network rendering, stale targets, .env payload, six compiled SFCs and canonical shell passed')
