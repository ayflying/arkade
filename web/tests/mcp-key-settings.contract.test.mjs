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

test('MCP settings compile and the one-time reveal is isolated in the admin tab', () => {
  const settingsPath = 'views/SettingsView.vue'
  const panelPath = 'components/admin/McpKeySettings.vue'
  for (const filename of [settingsPath, panelPath]) {
    const { descriptor, errors } = parse(read(filename), { filename })
    assert.deepEqual(errors, [])
    compileScript(descriptor, { id: filename })
    assert.deepEqual(compileTemplate({ source: descriptor.template.content, filename, id: filename }).errors, [])
  }
  assert.match(read('router/administration.ts'), /name: 'settings'.*adminOnly: true/)
  assert.match(read(settingsPath), /v-if="auth\.isAdmin" name="mcp"/)
  assert.match(read(settingsPath), /v-if="activeTab === 'mcp'" :key="auth\.user\?\.id"/)
  const panel = read(panelPath)
  assert.match(panel, /keys\.value = \[created\.key, \.\.\.keys\.value\]/)
  assert.match(panel, /function dismissToken\(\) \{\s*token\.value = ''/)
  assert.match(panel, /onBeforeUnmount\(\(\) => \{\s*disposed = true\s*dismissToken\(\)/)
  assert.match(panel, /<n-space v-if="token" vertical>/)
  assert.doesNotMatch(panel, /localStorage|sessionStorage/)
})
