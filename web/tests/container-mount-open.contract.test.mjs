/**
 * 容器挂载「打开卷文件」契约测试
 *
 * ## 这条契约在守什么
 *
 * 容器详情里的挂载可以直接点开，复用存储卷文件模块。但只有 **volume**
 * 类型有具名卷，bind/tmpfs 没有——它们的 Source 是宿主路径，文件模块
 * 整条链路（inspect 卷 → 拿 Mountpoint → bind 进 helper）只认卷名。
 *
 * 所以这里守三件事：
 * 1. 卷名来自后端的 `name` 字段，**绝不从 `source` 截路径反推**；
 * 2. 只有 volume + 非空卷名才给「打开文件」入口，其余如实标注不可用；
 * 3. 跳转目标就是现成的卷文件路由，不另建一套页面。
 */

import test from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const srcDir = join(here, '..', 'src')

const failures = []

function check(label, condition, detail) {
  if (condition) return
  failures.push(detail === undefined ? label : `${label}：${detail}`)
}

const tableSrc = readFileSync(
  join(srcDir, 'components', 'container', 'ContainerMountsTable.vue'),
  'utf8'
)
const viewSrc = readFileSync(join(srcDir, 'views', 'ContainerDetailView.vue'), 'utf8')
const typeSrc = readFileSync(join(srcDir, 'types', 'container.ts'), 'utf8')
const routerSrc = readFileSync(join(srcDir, 'router', 'index.ts'), 'utf8')

/* ───────── 1. 卷名是独立字段，不从路径猜 ───────── */

check(
  '类型: 挂载带可选 name 字段',
  /name\?: string/.test(typeSrc),
  '没有 name 就只能去解析 source，那一定会解析错'
)
check(
  '类型: 注释明确禁止从 source 反推卷名',
  /不要从 `source` 截取反推/.test(typeSrc)
)
check(
  '表格: 打开判据用 name，不是 source',
  /mount\.type === 'volume' && mount\.name !== ''/.test(tableSrc),
  '判据必须同时要求 volume 类型与非空卷名'
)
check(
  '表格: 不接受从 source 截取卷名',
  !/source\.split|source\.replace|basename/.test(tableSrc),
  '表格里出现了从 source 解析路径的代码'
)

/* ───────── 2. 不可用的挂载要如实标注 ───────── */

check('表格: 不可用的挂载不给按钮', /isOpenable\(row\)[\s\S]{0,120}NButton/.test(tableSrc))
check(
  '表格: 标注「不支持」而不是假装可点',
  /不支持/.test(tableSrc) && /无卷名/.test(tableSrc)
)
check(
  '表格: 向用户解释为什么 bind/tmpfs 不能打开',
  /文件模块只处理具名卷/.test(tableSrc)
)

/* ───────── 3. 跳转复用现有卷文件路由 ───────── */

check(
  '视图: 打开挂载跳转到卷文件路由',
  /name: 'agent-volume-files'/.test(viewSrc),
  '必须复用现成路由，不另建页面'
)
check('视图: 跳转参数带服务器与卷名', /params: \{ id: agentId\.value, name: mount\.name \}/.test(viewSrc))
check(
  '视图: 兜底再判一次 volume 与卷名',
  /mount\.type !== 'volume' \|\| \(mount\.name \?\? ''\) === ''/.test(viewSrc),
  '父组件也要挡住非法输入，不能只靠表格的按钮'
)
check('视图: 挂载表的 open 事件已接线', /@open="openMountFiles"/.test(viewSrc))
check(
  '路由: 卷文件页接收卷名参数',
  /path: 'volumes\/:name\/files'/.test(routerSrc) && /name: 'agent-volume-files'/.test(routerSrc)
)

test('容器挂载打开卷文件契约', () => {
  assert.deepEqual(failures, [], `\n${failures.join('\n')}`)
})
