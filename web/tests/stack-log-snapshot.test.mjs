import assert from 'node:assert/strict'
import { test } from 'node:test'
import { readStackLogSnapshot } from '../src/components/stack/stackLogSnapshot.ts'

const sources = Array.from({ length: 6 }, (_, i) => ({ id: String(i), name: `容器${i}` }))

test('汇总日志保持各容器顺序，单个容器失败不影响其他结果', async () => {
  const results = await readStackLogSnapshot(sources, async id => {
    if (id === '1') throw new Error('读取失败')
    return { lines: [`日志 ${id}`, '下一行'], truncated: id === '2' }
  }, () => true)
  assert.deepEqual(results.map(r => r.source), sources)
  assert.equal(results[1].error, '读取失败')
  assert.deepEqual(results[2].response, { lines: ['日志 2', '下一行'], truncated: true })
  assert.deepEqual(results[5].response.lines, ['日志 5', '下一行'])
})

test('切走后不再继续派发，最多四路并发', async () => {
  let current = true
  const finish = []
  const pending = readStackLogSnapshot(sources, () => new Promise(resolve => finish.push(resolve)), () => current)
  assert.equal(finish.length, 4)
  current = false
  for (const resolve of finish) resolve({ lines: [], truncated: false })
  await pending
  assert.equal(finish.length, 4)
})
