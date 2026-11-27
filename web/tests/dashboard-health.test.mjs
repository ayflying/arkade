import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { getAgentHealth, sampleExpiresAt } from '../src/utils/dashboardHealth.ts'

const now = 1_800_000_000_000
const current = {
  id: 'one', status: 'online', runtime_ok: true, sampled_at: now / 1000 - 15,
  host_metrics: {
    cpu_percent: 0,
    memory: { used_bytes: 512, total_bytes: 1024 },
    disk: { used_bytes: 256, total_bytes: 1024, scope: 'host_root' },
    network: { rx_bytes_per_sec: 2048, tx_bytes_per_sec: 0 },
    disk_io: { read_bytes_per_sec: 0, write_bytes_per_sec: 4096 }
  }
}
const row = (agent = current, time = now) => getAgentHealth(agent, time, now)

test('connection states override health samples; incidents sort ahead of healthy servers', () => {
  for (const status of ['offline', 'pending', 'upgrading']) {
    const item = row({ ...current, status })
    assert.equal(item.state.key, status)
    assert.equal(item.sample, null)
  }
  assert.equal(row({ ...current, status: 'offline', disabled: true }).state.key, 'disabled')
  assert.equal(row({ ...current, disabled: true }).sample, null)
  assert.equal(row({ ...current, status: 'unexpected' }).state.key, 'unknown')
  const engine = row({ ...current, runtime_ok: false })
  assert.equal(engine.state.key, 'engine-error')
  assert.ok(engine.sample, 'engine failure does not imply host sampling failure')
  assert.ok(engine.rank < row().rank)
  assert.ok(row({ ...current, status: 'offline' }).rank < row().rank)
})

test('legacy, engine-only and partial samples never fabricate health or usage', () => {
  assert.equal(row({ id: 'old', status: 'online' }).state.key, 'no-data')
  assert.equal(row({ ...current, runtime_ok: undefined }).state.key, 'engine-unknown')
  assert.equal(row({ ...current, host_metrics: undefined }).state.key, 'no-data')
  assert.equal(row({ ...current, runtime_ok: false, host_metrics: undefined }).state.key, 'engine-error')
  assert.equal(row({ ...current, sampled_at: undefined }).sample, null)
  assert.equal(row({ ...current, sampled_at: NaN }).runtimeOk, undefined)
  assert.equal(row().sample.cpuPercent, 0)
  assert.deepEqual(row().sample.memory, current.host_metrics.memory)
  assert.equal(row().sample.disk.scope, 'host_root')
  const partial = row({ ...current, host_metrics: {
    cpu_percent: -1, memory: { used_bytes: 120, total_bytes: 100 }, disk: { used_bytes: 0, total_bytes: 1024, scope: 'host_filesystem' }
  } })
  assert.equal(partial.sample.cpuPercent, undefined)
  assert.equal(partial.sample.memory, undefined)
  assert.equal(partial.sample.disk.scope, 'host_filesystem')
  assert.equal(partial.sample.disk.used_bytes, 0)
  assert.equal(row({ ...current, host_metrics: { disk: { used_bytes: 0, total_bytes: 100, scope: 'overlay' } } }).sample, null)
})

test('network and disk IO rates accept zero but never invent or invert a value', () => {
  // 0 B/s 是真实的「没有流量」，必须与「没采到」区分开，不能变成 undefined。
  assert.deepEqual(row().sample.network, { rx: 2048, tx: 0 })
  assert.deepEqual(row().sample.diskIO, { read: 0, write: 4096 })

  const bad = row({ ...current, host_metrics: {
    cpu_percent: 10,
    network: { rx_bytes_per_sec: -1, tx_bytes_per_sec: 10 },
    disk_io: { read_bytes_per_sec: Infinity, write_bytes_per_sec: 10 }
  } })
  assert.equal(bad.sample.network, undefined, 'a negative rate must not be shown as usage')
  assert.equal(bad.sample.diskIO, undefined, 'an infinite rate must not be shown as usage')

  const nan = row({ ...current, host_metrics: { network: { rx_bytes_per_sec: NaN, tx_bytes_per_sec: 1 } } })
  assert.equal(nan.sample, null)

  // 只有速率也构成有效采样：老的 Admin 后端没有 disk 挂载，但网络/IO 照样有值。
  const rateOnly = row({ ...current, host_metrics: { network: { rx_bytes_per_sec: 1, tx_bytes_per_sec: 2 } } })
  assert.equal(rateOnly.state.key, 'ready')
  assert.deepEqual(rateOnly.sample.network, { rx: 1, tx: 2 })
  assert.equal(rateOnly.sample.memory, undefined)
})

test('rate fields stay hidden for legacy agents and after a sample expires', () => {
  const legacy = row({ id: 'old', status: 'online', runtime_ok: true, sampled_at: current.sampled_at,
    host_metrics: { cpu_percent: 5 } })
  assert.equal(legacy.sample.network, undefined)
  assert.equal(legacy.sample.diskIO, undefined)

  const expiry = current.sampled_at * 1000 + 60_000
  assert.equal(row(current, expiry).sample, null, 'expired rates must not be presented as current')
})

test('metrics and engine state expire, including timestamps ahead of the browser clock', () => {
  const expiry = current.sampled_at * 1000 + 60_000
  assert.equal(row(current, expiry - 1).state.key, 'ready')
  assert.equal(row(current, expiry).state.key, 'no-data')
  assert.equal(row({ ...current, runtime_ok: false }, expiry).runtimeOk, undefined)
  const ahead = { ...current, sampled_at: now / 1000 + 86_400 }
  assert.equal(sampleExpiresAt(ahead.sampled_at, now), now + 60_000)
  assert.equal(row(ahead, now + 60_000).sample, null)
})

test('dashboard owns exactly one server list reader and has no single-server resource section', () => {
  const readSource = (path) => readFileSync(new URL(`../src/${path}`, import.meta.url), 'utf8')
  const page = readSource('views/OverviewView.vue')
  const sync = readSource('composables/useAgentStatusSync.ts')
  assert.equal([...page.matchAll(/await listAgents\(/g)].length, 1)
  assert.match(page, /usePolling\(refresh,/)
  assert.match(page, /aria-labelledby="fleet-heading"/)
  assert.doesNotMatch(page, /单台服务器资源|<n-select\b|<stat-card\b|loadAgentOverview|selectedAgentId/)
  assert.match(sync, /route\.name !== 'overview'/)
})
