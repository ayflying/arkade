import assert from 'node:assert/strict'
import { test } from 'node:test'
import { buildComposeSnippet, guessHubUrl, validateHubUrl } from '../src/utils/agentDeploy.ts'

const input = {
  agentName: '测试服务器',
  hubUrl: ' https://hub.example.com:8443/ ',
  token: 'test-token'
}

test('部署只需要 Compose，默认使用 latest、无前缀变量并保留现有挂载', () => {
  const compose = buildComposeSnippet(input)
  assert.match(compose, /^    image: ghcr\.io\/ayflying\/arkade-agent:latest$/m)
  assert.match(compose, /^    restart: unless-stopped$/m)
  assert.match(compose, /^      HUB_URL: "https:\/\/hub\.example\.com:8443"$/m)
  assert.match(compose, /^      AGENT_TOKEN: "test-token"$/m)
  assert.match(compose, /^      AGENT_NAME: "测试服务器"$/m)
  assert.match(compose, /^      DATA_DIR: \/opt\/arkade\/agent$/m)
  assert.match(compose, /^      RUNTIME_KIND: auto$/m)
  assert.match(compose, /^      LOG_LEVEL: info$/m)
  assert.match(compose, /^      - agent-data:\/opt\/arkade\/agent$/m)
  assert.match(compose, /^      - \/var\/run\/docker\.sock:\/var\/run\/docker\.sock$/m)
  assert.doesNotMatch(compose, /\.env\b|env_file|\.toml\b|ARKADE_|\/agent\/connect/)
  assert.match(buildComposeSnippet({ ...input, agentName: '' }), /^      AGENT_NAME: "arkade-agent"$/m)
})

test('名称中的引号、换行和美元符号不会改变 YAML 结构或被 Compose 展开', () => {
  const agentName = '测试\'" # ${AGENT_TOKEN}\n下一行'
  const compose = buildComposeSnippet({ ...input, agentName })
  const scalar = compose.match(/^      AGENT_NAME: (.+)$/m)?.[1]
  assert.equal(JSON.parse(scalar), '测试\'" # $${AGENT_TOKEN}\n下一行')
  assert.doesNotMatch(compose, /^下一行/m)
})

test('Hub 地址允许 HTTP(S) origin，可省略默认端口，可带末尾 /', () => {
  for (const url of [input.hubUrl, 'http://10.0.0.5:8080', 'https://hub.example.com', 'http://[::1]:8080/']) {
    assert.equal(validateHubUrl(url), '', url)
  }
  assert.match(buildComposeSnippet({ ...input, hubUrl: 'https://HUB.EXAMPLE.COM:443/' }),
    /^      HUB_URL: "https:\/\/hub\.example\.com"$/m)
})

test('Hub 地址拒绝路径、凭据、查询、片段以及格式错误的地址', () => {
  const invalidUrls = [
    '', 'hub.example.com:8443', 'ws://hub.example.com', 'wss://hub.example.com/agent/connect',
    'https://hub/agent/connect', 'https://hub//', 'https://hub/./', 'https://hub/a/..',
    'https://hub/%2e', 'https://hub\\', 'https:///hub',
    'https://user:pass@hub', 'https://@hub', 'https://hub?key=value', 'https://hub?',
    'https://hub#fragment', 'https://hub#', 'https://hu\nb', 'https://hub:', 'https://hub:65536',
    'https://', 'https:///'
  ]
  for (const url of invalidUrls) {
    assert.notEqual(validateHubUrl(url), '', JSON.stringify(url))
    assert.throws(() => buildComposeSnippet({ ...input, hubUrl: url }), undefined, JSON.stringify(url))
  }
})

test('默认 Hub 地址只取当前 HTTP(S) 页面的 origin，不带路径或参数', (t) => {
  assert.equal(guessHubUrl(), '')
  const window = { location: new URL('https://hub.example.com/agents?filter=offline#list') }
  globalThis.window = window
  t.after(() => { delete globalThis.window })
  assert.equal(guessHubUrl(), 'https://hub.example.com')
  window.location = new URL('http://127.0.0.1:8080/agents')
  assert.equal(guessHubUrl(), 'http://127.0.0.1:8080')
  window.location = new URL('file:///tmp/index.html')
  assert.equal(guessHubUrl(), '')
})
