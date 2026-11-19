/**
 * 服务器在线状态的「重新校准」测试
 *
 * 覆盖两类事实：
 * 1. 节流器行为（可注入时钟，直接测并发去重 / 节流跳过 / 失败可立即重试）；
 * 2. 接线契约（useCurrentAgent 必须挂上校准，store 必须走节流器而不是裸 refresh）。
 *
 * 为什么值得单独测：这是「页面一直显示服务器离线、操作按钮全灰」的根因。
 * 一旦有人把 useAgentStatusSync 的调用删掉，或把它退化成只在挂载时 refresh 一次，
 * 界面不会报错，只会静默回到旧的错误行为——契约测试就是防这个。
 *
 * 运行方式与 CI 一致：`node tests/agent-status-sync.test.mjs`
 */
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { createThrottledSync, SYNC_MIN_INTERVAL_MS } from '../src/utils/syncThrottle.ts'

const read = (name) => readFileSync(new URL(`../src/${name}`, import.meta.url), 'utf8')

/* ───────────────────────── 节流器行为 ───────────────────────── */

test('首次触发一定真正执行同步', async () => {
  let calls = 0
  const sync = createThrottledSync({ run: async () => { calls++ } })
  assert.equal(await sync.trigger(), true)
  assert.equal(calls, 1)
})

test('并发触发共享同一个在途请求，不会打出多条请求', async () => {
  let calls = 0
  let release = () => {}
  const gate = new Promise((resolve) => { release = resolve })
  const sync = createThrottledSync({ run: async () => { calls++; await gate } })

  const first = sync.trigger()
  const second = sync.trigger()
  release()

  assert.deepEqual(await Promise.all([first, second]), [true, true])
  assert.equal(calls, 1)
})

test('成功后最小间隔内跳过，不重复请求', async () => {
  let calls = 0
  let clock = 1000
  const sync = createThrottledSync({
    run: async () => { calls++ },
    minIntervalMs: 3000,
    now: () => clock
  })

  await sync.trigger()
  clock += 1000
  // 距上次成功只过了 1 秒：跳过，但语义上状态仍然可信
  assert.equal(await sync.trigger(), true)
  assert.equal(calls, 1)
})

test('超过最小间隔后会重新请求', async () => {
  let calls = 0
  let clock = 0
  const sync = createThrottledSync({
    run: async () => { calls++ },
    minIntervalMs: 3000,
    now: () => clock
  })

  await sync.trigger()
  clock += 3000
  await sync.trigger()
  assert.equal(calls, 2)
})

test('trigger(0) 绕过间隔强制同步（页面挂载校准用）', async () => {
  let calls = 0
  const sync = createThrottledSync({ run: async () => { calls++ }, minIntervalMs: 3000, now: () => 0 })

  await sync.trigger()
  await sync.trigger(0)
  assert.equal(calls, 2)
})

test('失败返回 false 且不推进节流时间：恢复后立刻重试', async () => {
  let calls = 0
  let shouldFail = true
  let clock = 0
  const sync = createThrottledSync({
    run: async () => { calls++; if (shouldFail) throw new Error('Hub 正在重启') },
    minIntervalMs: 3000,
    now: () => clock
  })

  assert.equal(await sync.trigger(), false)

  // 关键：失败后即使远未到间隔，也必须允许立即重试——Hub 刚恢复时最需要校准
  clock += 100
  shouldFail = false
  assert.equal(await sync.trigger(), true)
  assert.equal(calls, 2)
})

test('同步失败不把异常抛给轮询调用方', async () => {
  const sync = createThrottledSync({ run: async () => { throw new Error('boom') } })
  // 抛出去会被 usePolling 记为失败并退避；这里要求它自己消化掉
  await assert.doesNotReject(() => sync.trigger())
})

test('默认最小间隔是正数，避免退化成疯狂请求', () => {
  assert.ok(SYNC_MIN_INTERVAL_MS > 0)
  assert.ok(SYNC_MIN_INTERVAL_MS <= 10000)
})

/* ───────────────────────── 接线契约 ───────────────────────── */

test('所有服务器页面共用的 useCurrentAgent 会挂载状态校准', () => {
  const source = read('composables/useCurrentAgent.ts')
  assert.match(source, /import \{ useAgentStatusSync \} from '@\/composables\/useAgentStatusSync'/)
  // 必须在 useCurrentAgent 内部调用——而不是只在某个页面里调，否则会有页面漏接
  assert.match(source, /export function useCurrentAgent\(\)[\s\S]*?useAgentStatusSync\(\)/)
})

test('校准时机：挂载立即校准一次 + 按自动刷新节奏周期校准', () => {
  const source = read('composables/useAgentStatusSync.ts')
  assert.match(source, /onMounted\([\s\S]*?syncStatus\(0\)/)
  assert.match(source, /usePolling\(/)
  assert.match(source, /interval: STATUS_POLL_INTERVAL_MS/)
  // 挂载那次由 onMounted 负责，轮询不能 immediate，否则每个页面都多打一次请求
  assert.match(source, /immediate: false/)
})

test('store 的 syncStatus 走节流器，而不是页面各自裸调 refresh', () => {
  const source = read('stores/agents.ts')
  assert.match(source, /createThrottledSync\(statusSyncOptions\)/)
  assert.match(source, /minIntervalMs: SYNC_MIN_INTERVAL_MS/)
  assert.match(source, /function syncStatus\(minIntervalMs\?: number\): Promise<boolean>/)
  assert.match(source, /syncStatus,/)
})

test('静默刷新失败要被当成失败，不能推进节流时间戳', () => {
  const source = read('stores/agents.ts')
  // refresh(true) 失败是「返回 false」而不是抛错，必须显式转换，否则会被记成成功
  assert.match(source, /if \(!\(await refresh\(true\)\)\) throw new Error/)
})

test('校准是静默的：不显示 loading，也不写错误横幅', () => {
  const source = read('stores/agents.ts')
  // refresh(true) 的语义：silent 不置 loading、不写 errorText
  assert.match(source, /async function refresh\(silent = false\): Promise<boolean>/)
  assert.match(source, /if \(!silent\) \{\s*\n\s*loading\.value = true/)
})
