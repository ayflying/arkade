import { strict as assert } from 'node:assert'
import { readFileSync } from 'node:fs'
import { registerHooks } from 'node:module'
import { test } from 'node:test'
import { computed, effectScope, nextTick, ref } from 'vue'

const stub = (source) => `data:text/javascript;base64,${Buffer.from(source).toString('base64')}`
const requestStub = stub('export const httpGet = (...args) => globalThis.metricRequest(...args)')
const pollingStub = stub('export const usePolling = (...args) => globalThis.metricPolling(...args)')
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === './request' && context.parentURL?.endsWith('/api/containerMetrics.ts')) {
      return { url: requestStub, shortCircuit: true }
    }
    if (specifier === '@/composables/usePolling') return { url: pollingStub, shortCircuit: true }
    if (specifier.startsWith('@/')) return nextResolve(new URL(`../src/${specifier.slice(2)}.ts`, import.meta.url).href, context)
    if (specifier === './formatSize') return nextResolve('./formatSize.ts', context)
    return nextResolve(specifier, context)
  }
})

const { useContainerMetrics } = await import('../src/composables/useContainerMetrics.ts')
const { formatContainerMetrics, isContainerSampleStale, sampleExpiresAt } = await import('../src/utils/containerMetrics.ts')
const { ApiError } = await import('../src/types/api.ts')

const zeroSample = {
  container_id: 'abcdef123456abcdef123456',
  at: 1_700_000_000,
  stale: false,
  stats_available: true,
  cpu_percent: 0,
  memory_bytes: 0,
  network_available: true,
  rates_available: true,
  network_rx_bytes: 0,
  network_tx_bytes: 0,
  network_rx_per_second: 0,
  network_tx_per_second: 0
}

function snapshot(agentId, containers = [zeroSample]) {
  return { agent_id: agentId, stale_after_seconds: 90, containers }
}

function makeMetrics(t, id = 'server/a') {
  const scope = effectScope()
  t.after(() => scope.stop())
  const agent = ref(id)
  const enabled = ref(true)
  let poll
  let options
  globalThis.metricPolling = (task, opts) => { poll = task; options = opts }
  const metrics = scope.run(() => useContainerMetrics(agent, computed(() => enabled.value)))
  return { agent, enabled, metrics, poll, options }
}

test('real zeros, multi-core percentages, units, and small nonzero rates', () => {
  assert.deepEqual(formatContainerMetrics(zeroSample), {
    cpu: '0%', memory: '0 B', rxRate: '0 B/s', txRate: '0 B/s', rxTotal: '0 B', txTotal: '0 B',
    resourceNote: '', networkNote: ''
  })
  const values = formatContainerMetrics({ ...zeroSample, cpu_percent: 281.25, memory_bytes: 1024,
    network_rx_bytes: 1024 * 1024, network_rx_per_second: 0.04, network_tx_per_second: 2048 })
  assert.equal(values.cpu, '281.25%')
  assert.equal(values.memory, '1 KB')
  assert.equal(values.rxRate, '<1 B/s')
  assert.equal(values.txRate, '2 KB/s')
  assert.equal(values.rxTotal, '1 MB')
})

test('missing stats, unsupported network and first-sample rates never become zero', () => {
  const unsupported = formatContainerMetrics({ ...zeroSample, stats_available: false, network_available: false })
  assert.equal(unsupported.cpu, '—')
  assert.equal(unsupported.memory, '—')
  assert.equal(unsupported.rxRate, '—')
  assert.equal(unsupported.rxTotal, '—')
  assert.equal(unsupported.networkNote, '网络统计不可用')
  const first = formatContainerMetrics({ ...zeroSample, cpu_percent: undefined, rates_available: false })
  assert.equal(first.cpu, '—')
  assert.equal(first.rxRate, '—')
  assert.equal(first.rxTotal, '0 B')
  assert.equal(first.networkNote, '速率暂不可用')
  const partial = formatContainerMetrics({ ...zeroSample, network_rx_per_second: null, network_tx_per_second: 0 })
  assert.equal(partial.rxRate, '—')
  assert.equal(partial.txRate, '0 B/s')
})

test('staleness honors Hub markers and age without altering values', () => {
  const expiresAt = sampleExpiresAt(zeroSample, 90)
  assert.equal(isContainerSampleStale(zeroSample, expiresAt, expiresAt - 1), false)
  assert.equal(isContainerSampleStale(zeroSample, expiresAt, expiresAt), true)
  assert.equal(isContainerSampleStale({ ...zeroSample, stale: true }, expiresAt, expiresAt - 1), true)
  assert.equal(formatContainerMetrics({ ...zeroSample, stale: true }).rxRate, '0 B/s')
})

test('one encoded server request feeds all exact-ID lookups and concurrent refreshes', async (t) => {
  const paths = []
  const other = { ...zeroSample, container_id: 'a-different-container' }
  globalThis.metricRequest = async (path) => { paths.push(path); return snapshot('server/a', [zeroSample, other]) }
  const { metrics, poll, options, enabled } = makeMetrics(t)
  await Promise.all([metrics.refresh(), metrics.refresh(), poll()])
  assert.deepEqual(paths, ['/agents/server%2Fa/container-metrics'])
  assert.equal(metrics.byId.value.get(zeroSample.container_id), zeroSample)
  assert.equal(metrics.byId.value.get(other.container_id), other)
  assert.equal(metrics.byId.value.get(zeroSample.container_id.slice(0, 12)), undefined)
  assert.equal(options.immediate, false)
  assert.equal(options.enabled.value, true)
  enabled.value = false
  assert.equal(options.enabled.value, false)
})

test('server change discards in-flight result and empty/unsupported responses differ', async (t) => {
  let resolveOld
  globalThis.metricRequest = (path) => path.includes('/old/')
    ? new Promise((resolve) => { resolveOld = resolve })
    : Promise.resolve(snapshot('new', []))
  const { agent, metrics, poll, options } = makeMetrics(t, 'old')
  const old = metrics.refresh()
  agent.value = 'new'
  await nextTick()
  await metrics.refresh()
  resolveOld(snapshot('old'))
  await old
  assert.equal(metrics.byId.value.size, 0)
  assert.equal(metrics.emptyStatus.value, 'waiting')
  globalThis.metricRequest = () => Promise.reject(new ApiError(404, 'not_found', 'Not found'))
  await poll()
  assert.equal(metrics.emptyStatus.value, 'unsupported')
  assert.equal(options.enabled.value, false)
})

test('detail resolves exact route IDs and does not hide metrics when inspect returns 403', () => {
  const detail = readFileSync(new URL('../src/views/ContainerDetailView.vue', import.meta.url), 'utf8')
  assert.match(detail, /metricsEnabled = computed\(\(\) => activeTab\.value === 'overview' && containerId\.value !== ''\)/)
  assert.match(detail, /metricContainerId = computed\(\(\) => detail\.value\?\.id \|\| containerId\.value\)/)
  assert.match(detail, /byId\.value\.get\(metricContainerId\.value\)/)
  const overview = detail.split('<n-tab-pane name="overview"')[1].split('</n-tab-pane>')[0]
  assert.match(overview, /<container-detail-metrics[^>]+:sample="sample"/)
  assert.doesNotMatch(overview.match(/<container-detail-metrics[^>]+>/)[0], /v-if|forbidden/)
})

test('local expiry works without polling and a transient refresh failure keeps samples', async (t) => {
  t.mock.timers.enable({ apis: ['Date', 'setTimeout'], now: zeroSample.at * 1000 })
  globalThis.metricRequest = async () => snapshot('server/a')
  const { metrics, poll, enabled } = makeMetrics(t)
  await metrics.refresh()
  assert.equal(metrics.byStaleId.value.has(zeroSample.container_id), false)
  globalThis.metricRequest = () => Promise.reject(new ApiError(500, 'internal', 'Failure'))
  await assert.rejects(poll(), { status: 500 })
  assert.equal(metrics.byId.value.get(zeroSample.container_id), zeroSample)
  assert.equal(metrics.emptyStatus.value, 'error')
  enabled.value = false
  t.mock.timers.tick(90_000)
  assert.equal(metrics.byStaleId.value.has(zeroSample.container_id), true)
})
