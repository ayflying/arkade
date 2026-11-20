import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { isSelfUpdateSubmissionUnconfirmed, watchAgentSelfUpdate } from '../src/utils/agentSelfUpdate.ts'

const id = 'agent-local'
const before = '0.0.32'
const after = '0.0.33'
const agent = (version = before, status = 'online', agentId = id) => ({ id: agentId, version, status })
const options = (overrides) => ({
  agentId: id,
  beforeVersion: before,
  requestId: 'update-1',
  signal: new AbortController().signal,
  loadAgents: async () => [agent()],
  loadStatus: async () => ({ state: 'pending' }),
  intervalMs: 0,
  timeoutMs: 1000,
  ...overrides
})

test('仍在线的旧版本、其他服务器的新版本、目标离线的新版本均不算升级', async () => {
  const rounds = [[agent(), agent(after, 'online', 'other')], [agent(after, 'offline')], [agent(after)]]
  let calls = 0
  const result = await watchAgentSelfUpdate(options({
    loadAgents: async () => rounds[calls++],
    loadStatus: async () => ({ state: calls === 1 ? 'pending' : 'running' })
  }))
  assert.equal(calls, 3)
  assert.deepEqual(result, { kind: 'updated', agent: agent(after) })
})

test('助手明确失败时保留服务端提供的说明，即使版本读取暂不可用', async () => {
  const result = await watchAgentSelfUpdate(options({
    loadAgents: async () => { throw new Error('正在重连') },
    loadStatus: async (key) => {
      assert.equal(key, 'update-1')
      return { state: 'failed', message: '无法读取更新配置' }
    }
  }))
  assert.deepEqual(result, { kind: 'failed', message: '无法读取更新配置' })
})

test('同一轮已看见新版本在线时优先确认版本', async () => {
  const result = await watchAgentSelfUpdate(options({
    loadAgents: async () => [agent(after)],
    loadStatus: async () => ({ state: 'failed' })
  }))
  assert.deepEqual(result, { kind: 'updated', agent: agent(after) })
})

test('助手成功但版本仍一致：继续观察，不再请求已结束助手，超时只报未确认', async () => {
  let statusCalls = 0
  let listCalls = 0
  const result = await watchAgentSelfUpdate(options({
    timeoutMs: 100,
    intervalMs: 1,
    loadAgents: async () => { listCalls++; return [agent()] },
    loadStatus: async () => { statusCalls++; return { state: 'succeeded' } }
  }))
  assert.equal(statusCalls, 1)
  assert.ok(listCalls > 1)
  assert.deepEqual(result, { kind: 'unconfirmed', helperSucceeded: true, lastObservedVersion: before })
})

test('助手先结束、Agent 稍后才回报新版本，最终仍确认升级', async () => {
  let calls = 0
  const result = await watchAgentSelfUpdate(options({
    loadAgents: async () => [agent(++calls < 3 ? before : after)],
    loadStatus: async () => ({ state: 'succeeded' })
  }))
  assert.equal(calls, 3)
  assert.deepEqual(result, { kind: 'updated', agent: agent(after) })
})

test('两条读取路径的临时异常不会结束观察', async () => {
  let calls = 0
  const result = await watchAgentSelfUpdate(options({
    loadAgents: async () => {
      if (++calls === 1) throw new Error('Hub 重启')
      return [agent(after)]
    },
    loadStatus: async () => { throw new Error('暂时无法读取助手') }
  }))
  assert.equal(calls, 2)
  assert.equal(result.kind, 'updated')
})

test('断线没有请求 ID 时只读取列表', async () => {
  let statusCalls = 0
  const result = await watchAgentSelfUpdate(options({
    requestId: undefined,
    loadAgents: async () => [agent(after)],
    loadStatus: async () => { statusCalls++; return { state: 'failed' } }
  }))
  assert.equal(statusCalls, 0)
  assert.equal(result.kind, 'updated')
})

test('到达观察期限时中止在途读取', async () => {
  let readSignal
  const result = await watchAgentSelfUpdate(options({
    timeoutMs: 100,
    loadAgents: async (signal) => {
      readSignal = signal
      // 让 Node 事件循环保持活跃，并验证超时确实中止了 fetch 所用的信号。
      return new Promise((resolve) => {
        const timer = setTimeout(() => resolve([]), 1000)
        signal.addEventListener('abort', () => { clearTimeout(timer); resolve([]) }, { once: true })
      })
    }
  }))
  assert.ok(readSignal?.aborted)
  assert.deepEqual(result, { kind: 'unconfirmed', helperSucceeded: false, lastObservedVersion: undefined })
})

test('卸载后不再读取更新结果', async () => {
  const controller = new AbortController()
  const work = watchAgentSelfUpdate(options({
    intervalMs: 500,
    signal: controller.signal,
    loadAgents: () => assert.fail('取消后不应读取服务器列表')
  }))
  controller.abort()
  assert.deepEqual(await work, { kind: 'cancelled' })
})

test('只容忍断线及非业务的网关不可用，显式拒绝不会继续观察', () => {
  for (const [status, code] of [[401, 'unauthorized'], [403, 'forbidden'], [409, 'agent_offline'], [501, 'unsupported'], [503, 'runtime_failed'], [503, 'timeout'], [200, 'invalid_response']]) {
    assert.equal(isSelfUpdateSubmissionUnconfirmed(status, code), false, code)
  }
  for (const [status, code] of [[0, 'network'], [502, 'http_502'], [503, 'http_503'], [504, 'http_504']]) {
    assert.equal(isSelfUpdateSubmissionUnconfirmed(status, code), true, code)
  }
})

test('页面按后端契约查询指定请求，不从同版推断固定标签', () => {
  const read = (path) => readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
  const api = read('api/agents.ts')
  const view = read('views/AgentListView.vue')
  assert.match(api, /\/self-update\/\$\{encodeURIComponent\(requestId\)\}/)
  assert.match(view, /requestId = res\.request_id/)
  assert.match(view, /loadStatus: \(id, readSignal\) => getAgentSelfUpdateStatus\(agent\.id, id, readSignal\)/)
  assert.match(view, /未确认新版本上线/)
  assert.doesNotMatch(view, /镜像 tag 固定成了|已重新上线，但版本仍然是/)
})
