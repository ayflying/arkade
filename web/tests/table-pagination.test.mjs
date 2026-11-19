/**
 * 分页切片逻辑单测
 *
 * 测的是 web/src/composables/useLocalPager.ts 里的**纯函数**：
 * 容器 / 编排 / 镜像 / 网络 / 存储卷五张表共用这套切片规则，
 * 出错会表现为「少显示几条」或「翻页后空表」这类难察觉的问题，所以单独覆盖。
 *
 * 运行方式与 CI 一致：`node tests/table-pagination.test.mjs`
 * （CI 在 Web checks 里遍历 tests/*.test.mjs 执行）。
 *
 * 直接 import `.ts` 源码：CI 用的 Node 22+ 原生支持类型擦除，
 * 这样测的就是页面真正调用的那份实现，不是复制品。
 */
import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import path from 'node:path'

const here = path.dirname(fileURLToPath(import.meta.url))
const modulePath = path.join(here, '..', 'src', 'composables', 'useLocalPager.ts')

const { TABLE_PAGE_SIZE, slicePage, totalPageCount, clampPage, shouldResetPage } = await import(
  // Windows 路径需要转成 file:// URL 才能被 ESM 动态 import
  pathToFileURL(modulePath)
)

function pathToFileURL(p) {
  return new URL('file://' + p.replaceAll('\\', '/'))
}

const results = []
function test(name, fn) {
  try {
    fn()
    results.push({ name, ok: true })
  } catch (err) {
    results.push({ name, ok: false, message: err.message })
  }
}

/** 造 n 条假数据，值等于下标，便于断言切到哪一段 */
function rows(n) {
  return Array.from({ length: n }, (_, i) => ({ id: i }))
}

test('每页 20 条是本轮需求，后续若改动必须同步更新产品预期', () => {
  assert.equal(TABLE_PAGE_SIZE, 20)
})

test('第一页取前 20 条', () => {
  const page = slicePage(rows(52), 1)
  assert.equal(page.length, 20)
  assert.equal(page[0].id, 0)
  assert.equal(page[19].id, 19)
})

test('最后一页只返回剩余条目，不会补空行', () => {
  const page = slicePage(rows(52), 3)
  assert.equal(page.length, 12)
  assert.equal(page[0].id, 40)
  assert.equal(page[11].id, 51)
})

test('数据量刚好整页时没有多余的空页', () => {
  assert.equal(totalPageCount(40), 2)
  assert.equal(slicePage(rows(40), 2).length, 20)
})

test('总页数向上取整，空列表也算 1 页', () => {
  assert.equal(totalPageCount(0), 1)
  assert.equal(totalPageCount(1), 1)
  assert.equal(totalPageCount(21), 2)
  assert.equal(totalPageCount(41), 3)
})

test('页码越界时夹紧到最后一页，而不是返回空表', () => {
  // 场景：原来有 52 条停在第 3 页，删掉 20 条后只剩 32 条（2 页）
  const page = slicePage(rows(32), 3)
  assert.equal(page.length, 12)
  assert.equal(page[0].id, 20)
})

test('页码为 0 / 负数 / 小数时都归一到合法页', () => {
  assert.equal(slicePage(rows(50), 0)[0].id, 0)
  assert.equal(slicePage(rows(50), -3)[0].id, 0)
  assert.equal(slicePage(rows(50), 2.9)[0].id, 20)
})

test('空数据返回空数组，调用方无需判空', () => {
  assert.deepEqual(slicePage([], 1), [])
  assert.deepEqual(slicePage([], 5), [])
})

test('不修改入参（表格数据与统计共用同一份数组）', () => {
  const source = rows(5)
  slicePage(source, 2, 2)
  assert.equal(source.length, 5)
  assert.equal(source[0].id, 0)
})

test('clampPage 只有在真的越界时才回退', () => {
  assert.equal(clampPage(3, 52), 3)
  assert.equal(clampPage(3, 32), 2)
  assert.equal(clampPage(1, 0), 1)
})

test('切换服务器时重置页码，同一台服务器刷新时保留页码', () => {
  assert.equal(shouldResetPage('agent-a', 'agent-b'), true)
  assert.equal(shouldResetPage('agent-a', 'agent-a'), false)
})

const failed = results.filter((r) => !r.ok)
for (const r of results) {
  if (!r.ok) console.error(`✗ ${r.name}\n  ${r.message}`)
}
if (failed.length > 0) {
  console.error(`\n分页单测失败 ${failed.length}/${results.length}`)
  process.exit(1)
}
console.log(`✓ 分页单测通过，共 ${results.length} 项`)
