/**
 * 容器操作位置调整 + 批量 + 重建 契约测试
 *
 * 纯 Node，无框架。跑法：node tests/container-actions.contract.test.mjs
 *
 * 这轮改动有三条容易被"改回去"的约定，测试就是钉住它们：
 *
 * 1. **列表页不再有行内操作按钮**：操作全部收进详情页。
 *    如果以后有人图省事把按钮加回列表，"多选 + 批量"的交互就会被两套入口撕开。
 * 2. **批量重建默认不拉镜像**：拉取耗时不可控，会把整批卡在"没反应"的状态；
 *    拉最新镜像必须走详情页显式勾选。
 * 3. **重建会换容器 ID**：调用方必须用返回值替换旧引用，
 *    否则后续操作打到已删除的容器上，报"容器不存在"。
 */
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))

let passed = 0
const failures = []

function check(name, ok, detail) {
  if (ok) {
    passed += 1
  } else {
    failures.push(detail === undefined ? name : `${name} —— ${detail}`)
  }
}

const read = (...parts) => readFileSync(join(here, '..', ...parts), 'utf8')

const listSrc = read('src', 'views', 'ContainerListView.vue')
const detailSrc = read('src', 'views', 'ContainerDetailView.vue')
const apiSrc = read('src', 'api', 'containers.ts')
const composableSrc = read('src', 'composables', 'useContainers.ts')
const confirmSrc = read('src', 'utils', 'confirm.ts')

/* ───────── 列表页：去掉行内操作，改成多选 + 批量 + 搜索 ───────── */

check(
  '列表: 不再渲染行内操作组件',
  !/ContainerRowActions/.test(listSrc),
  '容器操作应只在详情页'
)
check(
  '列表: 没有「操作」列',
  !/title: '操作'/.test(listSrc),
  '行内按钮会让多选交互被两套入口撕开'
)
check(
  '列表: 有勾选列',
  /type: 'selection'/.test(listSrc),
  '批量操作需要多选'
)
check(
  '列表: 勾选列固定在左侧',
  /type: 'selection'[\s\S]{0,120}fixed: 'left'/.test(listSrc),
  '窄屏横向滚动时勾选列会滑出视口'
)
check(
  '列表: 勾选状态受控',
  /checked-row-keys/.test(listSrc) && /update:checked-row-keys/.test(listSrc)
)
check(
  '列表: 提供批量启动/停止/重启/删除/重建',
  ['handleBatchStart', 'handleBatchStop', 'handleBatchRestart', 'handleBatchDelete', 'handleBatchRecreate'].every(
    (fn) => new RegExp(`${fn}\\s*\\(`).test(listSrc)
  )
)
/*
 * 批量删除不能带删卷开关：数据丢失必须逐次显式确认，
 * 不能藏在"批量"这个动作背后。
 */
check(
  '列表: 批量删除不删数据卷',
  /removeVolumes: false/.test(composableSrc),
  '批量删卷会让一次误操作造成大面积数据丢失'
)
check(
  '列表: 有模糊搜索输入',
  /搜索名称 \/ 镜像 \/ ID/.test(listSrc) && /keyword/.test(listSrc)
)
check(
  '列表: 搜索匹配名称、镜像与 ID',
  /c\.name\.toLowerCase\(\)\.includes/.test(listSrc) &&
    /c\.image\.toLowerCase\(\)\.includes/.test(listSrc) &&
    /c\.id\.toLowerCase\(\)\.includes/.test(listSrc),
  '只匹配名称会让"我记得镜像是 nginx"这种找法失效'
)
check(
  '列表: 搜索无结果与没有容器分开提示',
  /没有匹配/.test(listSrc) && /该服务器上还没有容器/.test(listSrc),
  '两种情况混用会让用户以为容器被删了'
)
check('列表: 搜索后回到第 1 页', /watch\(keyword/.test(listSrc))

/* ───────── 批量执行：串行、失败不中断、结束后重载 ───────── */

check(
  '批量: 串行执行（for..of + await）',
  /for \(const container of targets\)[\s\S]{0,200}await runBatchItem/.test(composableSrc),
  '并行启停同一台机器上的容器会竞争端口，失败原因无法归因'
)
check(
  '批量: 单项失败不影响其余',
  /failed\.push/.test(composableSrc) && /outcome\.ok \+= 1/.test(composableSrc)
)
check(
  '批量: 结束后静默重载列表',
  /await load\(true\)/.test(composableSrc),
  '重建会换 ID，逐条 patch 容易漏'
)
check(
  '批量: 重建默认不拉镜像',
  /recreateOne\(container, false\)/.test(composableSrc),
  '批量拉镜像会让整批卡在不可预估的等待'
)
check(
  '批量: 删除运行中容器自动 force',
  /force: needsForceDelete\(container\.state\)/.test(composableSrc)
)

/* ───────── 详情页：承载全部单容器操作 + 重建对话框 ───────── */

check(
  '详情: 有启动/停止/重启/删除/重建入口',
  ['handleStart', 'handleStop', 'handleRestart', 'handleDelete', 'handleRecreate'].every((fn) =>
    new RegExp(`${fn}\\s*\\(`).test(detailSrc)
  )
)
check(
  '详情: 重建前有二次确认',
  /confirmWithOption\(/.test(detailSrc),
  '重建不可逆且会换 ID，必须确认'
)
check(
  '详情: 重建提供「重新拉取镜像」勾选项',
  /重新拉取镜像/.test(detailSrc)
)
check(
  '详情: 勾选拉镜像默认关闭',
  /optionDefault: false/.test(detailSrc),
  '默认拉取会改变运行版本，必须由用户主动选择'
)
check(
  '详情: 说明重建会换容器 ID 与丢日志',
  /容器 ID 变化/.test(detailSrc) && /日志从头开始/.test(detailSrc),
  '不说明会让用户以为只是重启了一次'
)
check(
  '详情: 说明数据卷与绑定目录保留',
  /数据卷与绑定目录中的数据保留/.test(detailSrc)
)
check(
  '详情: 说明已停止的容器重建后仍停止',
  /已停止的容器重建后仍是停止状态/.test(detailSrc),
  '无条件启动会让一个本该停着的服务自己上线'
)
check(
  '详情: 提示编排容器单独重建会被编排覆盖',
  /编排/.test(detailSrc) && /覆盖/.test(detailSrc),
  'compose 管理的容器重建后，下次 compose up 会覆盖，用户必须知道'
)
check(
  '详情: 重建成功后路由切到新 ID',
  /router\.replace\(/.test(detailSrc) && /container_id/.test(detailSrc),
  '留在旧 ID 上，下一次操作会报容器不存在'
)
check(
  '详情: 删除成功后返回列表',
  /已删除[\s\S]{0,200}back\(\)/.test(detailSrc),
  '容器已不存在，留在详情页只会显示 404'
)
check(
  '详情: 不可操作时把原因写在按钮旁',
  /block-reason/.test(detailSrc) && /blockReason/.test(detailSrc),
  '只放在 tooltip 里，触屏用户看不到'
)

/* ───────── 接口层：重建只在 true 时拼参数 ───────── */

check(
  '接口: 有重建接口',
  /export async function recreateContainer/.test(apiSrc)
)
check(
  '接口: 重建调用 /recreate',
  /\/recreate\$\{suffix\}/.test(apiSrc)
)
check(
  '接口: pull 只在 true 时拼参数',
  /options\.pull === true \? '\?pull=true' : ''/.test(apiSrc),
  '拼出 pull=false 或非法值会被后端 400，也可能让用户以为没拉'
)
check(
  '接口: 契约注释登记了 recreate',
  /\/recreate\?pull=/.test(apiSrc)
)

/* ───────── 确认框：支持分段说明 ───────── */

check(
  '确认框: 支持 notes 分段',
  /notes\?: string\[\]/.test(confirmSrc) && /confirm-with-option__note/.test(confirmSrc),
  '长说明挤成一段读不下去'
)

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 容器操作契约测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 容器操作契约测试：${passed} 通过，0 失败`)
