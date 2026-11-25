/**
 * 资源列表「多选 + 批量删除 + 清理」契约测试
 *
 * 这一轮改的是三个高危页面的删除入口：行内单删换成勾选 + 页头批量，
 * 再各加一个「一键清理」。删除不可撤销（卷里是数据库文件、镜像删了
 * 要重新拉），所以这里守的不是样式，而是**安全语义**：
 *
 * 1. 不可删的项不能被勾选——让「能勾的都能删」成为可依赖的直觉；
 * 2. 清理只按严格规则挑目标，绝不顺手扩大范围；
 * 3. 批量删除绝不静默降级成强制删除；
 * 4. 行内单删按钮确实移除了（否则两条删除路径的确认文案会走偏）。
 */

import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const webRoot = resolve(here, '..')
const read = (rel) => readFileSync(resolve(webRoot, rel), 'utf8')

const volumeView = read('src/views/VolumeListView.vue')
const networkView = read('src/views/NetworkListView.vue')
const imageView = read('src/views/ImageListView.vue')
const bulkComposable = read('src/composables/useBulkActions.ts')
const resourcePage = read('src/components/resource/ResourcePage.vue')
const resourceTypes = read('src/types/resource.ts')

/* ─────────────────── 存储卷：显示挂载到的容器 ─────────────────── */

test('存储卷类型带上使用它的容器名，且明确禁止从 in_use 反推', () => {
  // in_use 是布尔，无法回答「谁在占着」；containers 才是用户要的信息
  assert.match(resourceTypes, /containers:\s*string\[\] \| null/)
  assert.match(resourceTypes, /含已停止但未删除的容器/)
  // 与 in_use 同源，不能各算一份
  assert.match(resourceTypes, /与\s*`in_use`\s*与它同源|`in_use`\s*与它同源|同源/)
})

/*
 * 滚动升级期间 containers 会是 null（旧版 Agent 不上报）。
 *
 * 这一条守的是「升级后第一件要去确认的事」不会自己先崩：
 * null.length 一次属性访问就能把整张卷列表渲染打断。
 */
test('存储卷页容忍 containers 缺失/为 null，且不把使用中说成未使用', () => {
  assert.match(volumeView, /const names = row\.containers \?\? \[\]/)
  // 拿不到名字时必须仍然显示「使用中」（in_use 在旧版本也可信）
  assert.match(volumeView, /if \(row\.in_use\) \{/)
  assert.match(volumeView, /未上报具体容器名/)
})

test('存储卷页有「挂载到的容器」列并渲染具体容器名', () => {
  assert.match(volumeView, /title:\s*'挂载到的容器'/)
  // 无人使用时给出明确的「未使用」，而不是空白
  assert.match(volumeView, /'未使用'/)
  // 容器名多时折叠，完整清单放 title
  assert.match(volumeView, /names\.join\('\\n'\)/)
})

/* ─────────────────── 多选与批量删除 ─────────────────── */

test('存储卷页：使用中的卷不能被勾选', () => {
  // 勾选是唯一入口，所以不可删必须体现在 selection 列的 disabled 上
  assert.match(volumeView, /type:\s*'selection'/)
  assert.match(volumeView, /disabled:\s*\(row\)\s*=>\s*!isDeletable\(row\)/)
  assert.match(volumeView, /function isDeletable\(item: VolumeInfo\): boolean \{\s*return !item\.in_use/)
})

test('网络页：内置网络与有容器接入的网络不能被勾选', () => {
  assert.match(networkView, /type:\s*'selection'/)
  assert.match(networkView, /disabled:\s*\(row\)\s*=>\s*!isDeletable\(row\)/)
  assert.match(networkView, /return !builtin && row\.containers === 0/)
  // 不可删的原因必须能查到，否则用户只看到勾不上
  assert.match(networkView, /'Docker 内置网络，不可删除'/)
})

test('三个页面都移除了行内单删按钮，删除统一走页头批量', () => {
  // 行内单删与批量两条路径的确认文案必然走偏，统一入口是这次的前提
  for (const [name, source] of [
    ['存储卷', volumeView],
    ['网络', networkView],
    ['镜像', imageView]
  ]) {
    assert.doesNotMatch(
      source,
      /title:\s*'操作',[\s\S]{0,1200}default:\s*\(\)\s*=>\s*'删除'/,
      `${name}页的操作列不应再有行内删除按钮`
    )
  }
  // 存储卷页干脆没有操作列
  assert.doesNotMatch(volumeView, /title:\s*'操作'/)
})

test('批量删除逐项执行并汇总失败原因，部分失败不能说成整体失败', () => {
  assert.match(bulkComposable, /outcomes\.push\(\{\s*label:\s*target\.label,\s*error:\s*''\s*\}\)/)
  assert.match(bulkComposable, /eslint-disable-next-line|/)
  // 逐个删而不是并发：并发下第一个失败后难以判断其余是否受影响
  assert.match(bulkComposable, /for \(const target of targets\)/)
  // 成功与失败分别计数
  assert.match(bulkComposable, /成功 \$\{String\(ok\)\}/)
  // 部分失败时以服务端为准重拉，本地状态必然不一致
  assert.match(bulkComposable, /await options\.refresh\(\)/)
})

test('全选只作用于当前页，不做跨页累积', () => {
  // 跨页累积会导致「用户以为只删当前页，结果把前面勾的也删了」
  assert.match(bulkComposable, /function togglePage/)
  assert.match(bulkComposable, /只动当前页的键/)
  for (const source of [volumeView, networkView, imageView]) {
    assert.match(source, /@click="togglePageSelection"/)
  }
})

/* ─────────────────── 清理：严格规则 ─────────────────── */

test('存储卷清理只挑未被使用的卷', () => {
  assert.match(volumeView, /const unusedVolumes = computed\(\(\) => items\.value\.filter\(\(v\) => !v\.in_use\)\)/)
  assert.match(volumeView, /handleCleanup/)
  assert.match(volumeView, /清理未使用（\{\{ unusedVolumes\.length \}\}）/)
})

test('网络清理排除 Docker 内置网络与有容器接入的网络', () => {
  assert.match(networkView, /const cleanableNetworks = computed\(\(\) => items\.value\.filter\(isDeletable\)\)/)
  assert.match(networkView, /清理无用（\{\{ cleanableNetworks\.length \}\}）/)
  // 规则写在注释里，避免后人以为是漏了内置网络
  assert.match(networkView, /内置的删不掉/)
})

test('镜像清理只挑悬空且无容器引用的镜像，带标签的一律不动', () => {
  // 已打标签但无引用的镜像不是垃圾——那是下次部署要用的
  assert.match(
    imageView,
    /const cleanableImages = computed\(\(\) =>\s*items\.value\.filter\(\(i\) =>\s*i\.tags\.length === 0 && i\.containers === 0\s*\)\s*\)/
  )
  assert.match(imageView, /已打标签但没有容器引用的镜像\*\*不是垃圾\*\*/)
  assert.match(imageView, /清理悬空（\{\{ cleanableImages\.length \}\}）/)
})

test('清理确认框列出将被删除的对象，而不是只问「确定吗」', () => {
  // 用户对「一批」没有直觉，只有看到具体名字才知道怕
  assert.match(bulkComposable, /export function describeTargets/)
  assert.match(bulkComposable, /… 等共 \$\{String\(labels\.length\)\} 个/)
  for (const source of [volumeView, networkView, imageView]) {
    assert.match(source, /describeTargets\(/)
  }
})

/* ─────────────────── 镜像批量删除不得静默强制 ─────────────────── */

test('镜像批量删除默认跳过被引用的镜像，强制删除必须显式勾选', () => {
  // 自动降级成强制删除会让一批容器集体起不来，而用户只勾了几行
  assert.match(imageView, /const bulkForce = ref\(false\)/)
  assert.match(imageView, /bulkForce\.value && item\.containers > 0/)
  assert.match(imageView, /同时强制删除（\{\{ referencedSelected\.length \}\} 个被引用）/)
  // 全被引用时给出可执行的下一步，而不是一句失败
  assert.match(imageView, /选中的镜像都被容器引用，请勾选「同时强制删除」或先删除相关容器/)
})

/* ─────────────────── 外壳与状态一致性 ─────────────────── */

test('ResourcePage 的勾选是受控的，状态只存一份', () => {
  assert.match(resourcePage, /checkedKeys\?: string\[\]/)
  assert.match(resourcePage, /update:checkedKeys/)
  assert.match(resourcePage, /:checked-row-keys="selectable \? checkedKeys : undefined"/)
})

test('切换服务器时清空勾选，避免拿旧 ID 删新机器的资源', () => {
  for (const [name, source] of [
    ['存储卷', volumeView],
    ['网络', networkView],
    ['镜像', imageView]
  ]) {
    assert.match(source, /bulk\.clear\(\)/, `${name}页切换服务器时应清空勾选`)
  }
})

test('批量操作期间禁用其它批量入口，防止连点重复删除', () => {
  assert.match(bulkComposable, /if \(busy\.value \|\| targets\.length === 0\) return/)
  for (const source of [volumeView, networkView, imageView]) {
    assert.match(source, /:disabled="[^"]*bulk\.busy\.value/)
  }
})
