import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
import { parse, compileScript, compileTemplate } from 'vue/compiler-sfc'

const read = path => readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
const encode = js => `data:text/javascript;base64,${Buffer.from(js).toString('base64')}`
const transpile = path => ts.transpileModule(read(path), {
  compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 }
}).outputText

test('MCP key management follows the Hub API contract', async () => {
  const key = { id: 'some/id', name: '值班助手', prefix: 'test-prefix', createdAt: '2026-11-19T03:00:00Z' }
  const token = 'synthetic-one-time-key'
  const request = encode(`
    export const calls = []
    export const httpGet = async path => { calls.push(['GET', path]); return ${JSON.stringify({ items: [key] })} }
    export const httpPost = async (path, body) => { calls.push(['POST', path, body]); return ${JSON.stringify({ key, token })} }
    export const httpDelete = async path => { calls.push(['DELETE', path]) }
  `)
  const mocked = await import(request)
  const api = await import(encode(transpile('api/mcpKeys.ts').replace('./request', request)))
  const listed = await api.listMcpKeys()
  assert.deepEqual(listed, [key])
  assert.equal(JSON.stringify(listed).includes(token), false)
  assert.deepEqual(await api.createMcpKey(key.name), { key, token })
  assert.equal(await api.revokeMcpKey(key.id), undefined)
  assert.deepEqual(mocked.calls, [
    ['GET', '/mcp/keys'],
    ['POST', '/mcp/keys', { name: '值班助手' }],
    ['DELETE', '/mcp/keys/some%2Fid']
  ])
})

test('MCP config points to the same-origin endpoint and uses a bearer header', async () => {
  const { mcpEndpoint, mcpClientConfig, mcpKeyName } = await import(encode(transpile('utils/mcpClientConfig.ts')))
  const url = mcpEndpoint('https://arkade.example.test:9043')
  assert.equal(url, 'https://arkade.example.test:9043/mcp')
  assert.deepEqual(JSON.parse(mcpClientConfig(url)).mcpServers.arkade, {
    type: 'http', url, headers: { Authorization: 'Bearer <MCP_KEY>' }
  })
  assert.equal(JSON.parse(mcpClientConfig(url, 'once')).mcpServers.arkade.headers.Authorization, 'Bearer once')
  assert.equal(mcpKeyName('  值班助手\u00a0'), '值班助手')
  assert.equal(mcpKeyName(''), null)
  assert.equal(mcpKeyName('a\tb'), null)
  assert.equal(mcpKeyName('🛠'.repeat(80)), '🛠'.repeat(80))
  assert.equal(mcpKeyName('🛠'.repeat(81)), null)
})

test('MCP key management is available from personal settings only', () => {
  const accountPath = 'views/AccountView.vue'
  const settingsPath = 'views/SettingsView.vue'
  const panelPath = 'components/admin/McpKeySettings.vue'
  for (const filename of [accountPath, settingsPath, panelPath]) {
    const { descriptor, errors } = parse(read(filename), { filename })
    assert.deepEqual(errors, [])
    compileScript(descriptor, { id: filename })
    assert.deepEqual(compileTemplate({ source: descriptor.template.content, filename, id: filename }).errors, [])
  }
  const routes = read('router/administration.ts')
  const accountRoute = routes.split('\n').find(line => line.includes("name: 'account'"))
  assert.match(accountRoute, /path: 'account'/)
  assert.doesNotMatch(accountRoute, /adminOnly|public/)
  assert.match(read('components/common/UserMenu.vue'), /key === 'account'\) void router\.push\('\/account'\)/)
  assert.match(read(accountPath), /<n-card v-if="loaded && auth\.user && !auth\.user\.must_change_password" title="MCP 接入">\s*<mcp-key-settings :key="auth\.user\.id"/)
  assert.match(routes, /name: 'settings'.*adminOnly: true/)
  // 密钥按用户隔离，系统设置里不能再出现 MCP 入口（否则会被当成全局配置）。
  // 注释里允许提到 MCP，这里只看有没有真的渲染入口。
  assert.doesNotMatch(read(settingsPath), /McpKeySettings|name="mcp"|mcp-key-settings/)
  assert.doesNotMatch(read(settingsPath), /useAuthStore/)
})

test('shared MCP settings explain effective permissions and keep the one-time reveal local', () => {
  const panel = read('components/admin/McpKeySettings.vue')
  assert.match(panel, /MCP 客户端使用你当前的权限/)
  assert.match(panel, /cause\.status === 403\) return '当前账户无权执行此操作。'/)
  assert.doesNotMatch(panel, /管理员/)
  assert.match(panel, /keys\.value = \[created\.key, \.\.\.keys\.value\]/)
  assert.match(panel, /function dismissToken\(\) \{\s*token\.value = ''/)
  assert.match(panel, /onBeforeUnmount\(\(\) => \{\s*disposed = true\s*dismissToken\(\)/)
  assert.match(panel, /<n-space v-if="token" vertical>/)
  assert.doesNotMatch(panel, /localStorage|sessionStorage/)
})
