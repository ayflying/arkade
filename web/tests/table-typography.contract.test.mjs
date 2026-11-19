/**
 * 表格字号统一契约测试
 *
 * 纯 Node，无框架。跑法：node tests/table-typography.contract.test.mjs
 *
 * 背景：容器列表的主名称曾用 15px，编排列表的主名称用 12px 等宽灰字，
 * 服务器列表又是不设字号的默认 14px。同一个人在三个列表之间切换，
 * 会看到三种"名字"，还以为自己缩放错了。现在约定成两档：
 *
 * - **主名称**（容器名 / 编排名）= `.arkade-cell-name`，14px + 中粗，全站唯一档位；
 * - **技术标识**（镜像 / ID / 挂载路径 / 端口）= `.arkade-cell-mono`，12px + 次要色。
 *
 * 这个测试钉的就是这两档：字号本身、以及"哪些列该用哪一档"。
 * 谁再往某个列表里塞第三种字号，CI 会直接失败。
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

const globalCss = read('src', 'styles', 'global.css')
const containerList = read('src', 'views', 'ContainerListView.vue')
const stackList = read('src', 'components', 'stack', 'StackUnifiedTable.vue')
const stackContainers = read('src', 'components', 'stack', 'StackContainersTable.vue')

/** 取出某个 CSS 类的声明块（用于断言字号，而不是全文搜索） */
function cssBlock(css, className) {
  const start = css.indexOf(`.${className} {`)
  if (start === -1) return ''
  const end = css.indexOf('}', start)
  return css.slice(start, end)
}

/* ───────── 两档字号本身 ───────── */

const nameBlock = cssBlock(globalCss, 'arkade-cell-name')
const monoBlock = cssBlock(globalCss, 'arkade-cell-mono')

check('.arkade-cell-name 存在', nameBlock !== '', '主名称样式被删了，各列表会退回默认字号')
check(
  '.arkade-cell-name 是 14px',
  /font-size:\s*14px/.test(nameBlock),
  '主名称必须与表格 medium 正文一致；改成 15px 会比同表其它文字更显眼'
)
check(
  '.arkade-cell-name 保留中粗',
  /font-weight:\s*500/.test(nameBlock),
  '靠字号区分会让名称和正文一样平淡，靠字重即可'
)
check(
  '.arkade-cell-name 用等宽字体',
  /font-family:\s*var\(--arkade-mono-font\)/.test(nameBlock),
  '名称是技术字符串，等宽更利于区分 - 与 _'
)
check(
  '.arkade-cell-mono 是 12px',
  /font-size:\s*12px/.test(monoBlock),
  '技术标识比名称小一档是刻意层级，不能随手改大'
)

/* ───────── 哪些列用哪一档 ───────── */

check(
  '容器列表: 名称用主名称档',
  /'arkade-link arkade-cell-name'/.test(containerList)
)
check(
  '编排列表: 名称用主名称档',
  /'arkade-link arkade-cell-name'/.test(stackList),
  '编排列表名称用 mono 会让整页比容器列表小一号'
)
/*
 * 反向断言：编排列表的名称列不能再退回 mono。
 * 单独找渲染处，避免把"别的列用 mono"误判成违规。
 *
 * 先剥注释：这次的修复注释里就写着"之前这里用 arkade-cell-mono"，
 * 不剥的话测试会把说明文字当成违规代码（第一版就是这么误报的）。
 */
const stripComments = (src) =>
  src.replace(/\/\*[\s\S]*?\*\//g, '').replace(/\/\/[^\n]*/g, '')
const stackNameRender = stripComments(
  stackList.slice(stackList.indexOf("title: '名称'"), stackList.indexOf("title: '状态'"))
)
check(
  '编排列表: 名称渲染不再用 mono',
  stackNameRender !== '' && !/arkade-cell-mono/.test(stackNameRender),
  '名称仍用 12px 等宽灰字'
)
const stackContainersName = stripComments(
  stackContainers.slice(
    stackContainers.indexOf("title: '名称'"),
    stackContainers.indexOf("title: '镜像'")
  )
)
check(
  '编排详情容器表: 名称用主名称档',
  /arkade-cell-name/.test(stackContainersName),
  '同一个容器对象，在编排详情与容器列表里的字号应一致'
)

/* ───────── 镜像属于技术标识 ───────── */

const containerImageRender = containerList.slice(
  containerList.indexOf("title: '镜像'"),
  containerList.indexOf("title: '状态'")
)
check(
  '容器列表: 镜像用技术标识档',
  /arkade-cell-mono/.test(containerImageRender),
  '镜像在编排详情里是 12px 等宽，这里用正文 14px 会让同字段两页两种字号'
)
const stackContainersImage = stackContainers.slice(
  stackContainers.indexOf("title: '镜像'"),
  stackContainers.indexOf("title: '端口'") === -1
    ? stackContainers.length
    : stackContainers.indexOf("title: '端口'")
)
check(
  '编排详情容器表: 镜像用技术标识档',
  /arkade-cell-mono/.test(stackContainersImage)
)

/* ───────── 别再出现第三个字号 ───────── */

check(
  '没有其它地方给名称硬编码 15px',
  !/font-size:\s*15px/.test(globalCss),
  '15px 是这轮被统一掉的旧值，出现就是又漂了'
)

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 表格字号契约测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 表格字号契约测试：${passed} 通过，0 失败`)
