import assert from 'node:assert/strict'
import { test } from 'node:test'
import { fetchRuntimeVersion } from '../src/api/runtimeVersion.ts'

test('运行版本来自无缓存的健康检查，失败时不显示假版本', async (t) => {
  const signal = new AbortController().signal
  const fetchMock = t.mock.method(globalThis, 'fetch', async () => Response.json({ version: '0.0.22' }))
  assert.equal(await fetchRuntimeVersion(signal), '0.0.22')
  assert.deepEqual(fetchMock.mock.calls[0].arguments, ['/healthz', { cache: 'no-store', signal }])
  for (const response of [new Response('', { status: 503 }), new Response('<html>'), Response.json(null), Response.json({ version: '' })]) {
    fetchMock.mock.mockImplementationOnce(async () => response)
    assert.equal(await fetchRuntimeVersion(signal), null)
  }
  fetchMock.mock.mockImplementationOnce(async () => { throw new TypeError('offline') })
  assert.equal(await fetchRuntimeVersion(signal), null)
})
