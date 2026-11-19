import assert from 'node:assert/strict'
import { test } from 'node:test'
import { buildComposeSnippet, validateHubUrl } from '../src/utils/agentDeploy.ts'

const input = {
  agentName: '测试服务器',
  hubUrl: 'wss://hub.example.com/agent/connect',
  token: 'test-token'
}

test('部署只需要 Compose，并默认使用发布镜像的 latest 标签', () => {
  const compose = buildComposeSnippet(input)
  assert.match(compose, /^    image: ghcr\.io\/ayflying\/arkade-agent:latest$/m)
  assert.match(compose, /^      ARKADE_HUB_URL: "wss:\/\/hub\.example\.com\/agent\/connect"$/m)
  assert.match(compose, /^      ARKADE_AGENT_TOKEN: "test-token"$/m)
  assert.match(compose, /^      - agent-data:\/opt\/arkade\/agent$/m)
  assert.doesNotMatch(compose, /\.env\b|env_file|\.toml\b|ARKADE_AGENT_HUB_URL/)
})

test('名称中的引号、换行和美元符号不会改变 YAML 结构或被 Compose 展开', () => {
  const agentName = '测试\'" # ${ARKADE_AGENT_TOKEN}\n下一行'
  const compose = buildComposeSnippet({ ...input, agentName })
  const scalar = compose.match(/^      ARKADE_AGENT_NAME: (.+)$/m)?.[1]
  assert.equal(JSON.parse(scalar), '测试\'" # $${ARKADE_AGENT_TOKEN}\n下一行')
  assert.doesNotMatch(compose, /^下一行/m)
})

test('Hub 地址拒绝凭据和内嵌换行', () => {
  assert.equal(validateHubUrl(input.hubUrl), '')
  assert.notEqual(validateHubUrl('ws://user:pass@hub/agent/connect'), '')
  assert.notEqual(validateHubUrl('ws://hu\nb/agent/connect'), '')
})
