/**
 * 侧边菜单「当前服务器」标题测试
 *
 * 跑法：node tests/menu-name.contract.test.mjs
 *
 * ## 为什么这件事值得测
 *
 * 侧边栏原来固定显示「当前服务器」，用户在多台机器之间切换时看不出
 * 到底在哪台上操作——下错命令的代价很高。改成显示真实服务器名后，
 * 又引入两个新风险，正是这里要钉住的：
 *
 * 1. **显示旧名字**：服务器列表是异步的，切服务器时 name 可能还是上一台。
 *    显示 id 是诚实的，显示旧名字是危险的。
 * 2. **长名字撑破侧边栏**：名字可以很长，必须有 ellipsis + title。
 */
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import ts from 'typescript'

const here = dirname(fileURLToPath(import.meta.url))
const SRC_ROOT = join(here, '..', 'src')

/**
 * 加载 TS 源码：transpile 后把说明符换成 file URL，再 dataURL import。
 *
 * `@/` 别名手写映射到 src/；`.vue` 无法被 Node 直接加载，
 * 用一个空 stub 顶替——本测试只关心菜单结构，不渲染图标。
 */
const STUB = `data:text/javascript,${encodeURIComponent('export default {}')}`

async function loadTs(relPath) {
  const src = readFileSync(join(here, '..', relPath), 'utf8')
  const js = ts.transpileModule(src, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 }
  }).outputText
  const rewritten = js.replace(/from\s+['"]([^'"]+)['"]/g, (whole, spec) => {
    if (spec.endsWith('.vue')) return `from '${STUB}'`
    if (spec.startsWith('@/')) {
      const target = spec.slice(2)
      if (target.endsWith('.vue')) return `from '${STUB}'`
      return `from '${pathToFileURL(join(SRC_ROOT, target.endsWith('.ts') ? target : `${target}.ts`)).href}'`
    }
    if (spec.startsWith('.') || spec.includes(':')) return whole
    return `from '${import.meta.resolve(spec)}'`
  })
  return import(`data:text/javascript;base64,${Buffer.from(rewritten, 'utf8').toString('base64')}`)
}

const { buildMenuOptions, AGENT_OPERATION_ITEMS, isGlobalMenuKey } = await loadTs(
  'src/layouts/menuConfig.ts'
)

let passed = 0
const failures = []

function check(name, ok, detail) {
  if (ok) passed += 1
  else failures.push(detail === undefined ? name : `${name} —— ${detail}`)
}

/** 取出操作分组的 label 文字（label 可能是函数） */
function groupLabel(options, key) {
  const group = options.find((o) => o.key === key)
  if (group === undefined) return null
  const label = group.label
  if (typeof label === 'function') {
    const vnode = label()
    // h('span', {title}, name) 的 children 就是名字
    return vnode?.children
  }
  return typeof label === 'string' ? label : null
}

/** 取出 label 渲染出的 title 属性 */
function groupLabelTitle(options, key) {
  const group = options.find((o) => o.key === key)
  if (group === undefined || typeof group.label !== 'function') return null
  const vnode = group.label()
  return vnode?.props?.title
}

/* ───────── 1. 真实服务器名出现在分组标题 ───────── */

{
  const options = buildMenuOptions('ag-1', '生产环境-上海')
  check('名字: 有操作分组', options.some((o) => o.key === 'agent-operations'))
  check(
    '名字: 标题显示真实服务器名',
    groupLabel(options, 'agent-operations') === '生产环境-上海',
    String(groupLabel(options, 'agent-operations'))
  )
  check(
    '名字: 不再显示固定文案「当前服务器」',
    groupLabel(options, 'agent-operations') !== '当前服务器',
    '固定文案正是要修掉的问题'
  )
}

/* ───────── 2. 空名字退回 id，绝不沿用旧名 ───────── */

{
  const options = buildMenuOptions('ag-2')
  check(
    '兜底: 未传名字时显示 id',
    groupLabel(options, 'agent-operations') === 'ag-2',
    String(groupLabel(options, 'agent-operations'))
  )

  const blank = buildMenuOptions('ag-3', '')
  check('兜底: 空串显示 id', groupLabel(blank, 'agent-operations') === 'ag-3')

  const spaces = buildMenuOptions('ag-4', '   ')
  check(
    '兜底: 纯空格也显示 id',
    groupLabel(spaces, 'agent-operations') === 'ag-4',
    String(groupLabel(spaces, 'agent-operations'))
  )
}

/* ───────── 3. 切换服务器时名字跟着变 ───────── */

{
  const a = buildMenuOptions('ag-1', '服务器A')
  const b = buildMenuOptions('ag-2', '服务器B')
  check('切换: A 显示 A', groupLabel(a, 'agent-operations') === '服务器A')
  check('切换: B 显示 B', groupLabel(b, 'agent-operations') === '服务器B')
  check(
    '切换: 不会残留上一台的名字',
    groupLabel(b, 'agent-operations') !== '服务器A',
    '显示旧名字会让用户在错误机器上操作'
  )

  // id 变了但名字没跟上（异步滞后）→ 必须显示新 id，不能显示旧名
  const stale = buildMenuOptions('ag-9', '')
  check(
    '切换: 名字滞后时显示新 id 而非旧名',
    groupLabel(stale, 'agent-operations') === 'ag-9',
    String(groupLabel(stale, 'agent-operations'))
  )
}

/* ───────── 4. 未选中服务器时不渲染操作分组 ───────── */

{
  const none = buildMenuOptions(null)
  check(
    '无服务器: 不出现操作分组',
    !none.some((o) => o.key === 'agent-operations'),
    '未选中服务器时不该显示"当前服务器"分组'
  )
  check('无服务器: 仍有全局入口', none.some((o) => o.key === 'entry'))

  // 传了名字但没有 id：仍然不渲染（id 是唯一开关）
  const nameOnly = buildMenuOptions(null, '幽灵服务器')
  check(
    '无服务器: 只给名字也不渲染分组',
    !nameOnly.some((o) => o.key === 'agent-operations')
  )
}

/* ───────── 5. 长名字有 title 且内容被转义 ───────── */

{
  const long = '这是一个非常非常长的服务器名称'.repeat(6)
  const options = buildMenuOptions('ag-1', long)
  check(
    '长名: title 提供完整名',
    groupLabelTitle(options, 'agent-operations') === long,
    '没有 title 时被截断后看不全'
  )
  check(
    '长名: 正文仍是完整字符串原值',
    groupLabel(options, 'agent-operations') === long
  )

  /*
   * 名字是用户输入，可能含 HTML。
   * label 用 h('span', {}, name) 渲染 → name 作为文本子节点，
   * 不是 innerHTML，天然转义。这里断言传进去的是**字符串**而不是
   * 预解析的 HTML 片段。
   */
  const evil = '<img src=x onerror=alert(1)>'
  const evilOptions = buildMenuOptions('ag-1', evil)
  const vnode = evilOptions.find((o) => o.key === 'agent-operations').label()
  check(
    '转义: 名字作为纯文本 child 传入',
    vnode.children === evil,
    '若被当 HTML 解析就有注入风险'
  )
  check(
    '转义: 没有使用 innerHTML/dangerouslySetInnerHTML',
    !JSON.stringify(Object.keys(vnode.props ?? {})).includes('innerHTML'),
    '用 h() 文本 child 渲染即可天然转义'
  )
}

/* ───────── 6. 菜单项本身没被破坏 ───────── */

{
  const options = buildMenuOptions('ag-1', 'A')
  const ops = options.find((o) => o.key === 'agent-operations')
  check(
    '菜单项: 操作项数量不变',
    ops.children.length === AGENT_OPERATION_ITEMS.length,
    `${String(ops.children.length)} vs ${String(AGENT_OPERATION_ITEMS.length)}`
  )
  check(
    '菜单项: key 仍是路由名',
    ops.children.map((c) => c.key).join(',') ===
      AGENT_OPERATION_ITEMS.map((i) => i.routeName).join(',')
  )
  check('菜单项: 全局入口可跳转', isGlobalMenuKey('overview') === true)
  check('菜单项: 操作项不是全局入口', isGlobalMenuKey('agent-containers') === false)
}

/* ───────── 7. 接线：两处菜单都传了名字 ───────── */

{
  const layout = readFileSync(join(here, '..', 'src', 'layouts', 'AppLayout.vue'), 'utf8')
  const drawer = readFileSync(
    join(here, '..', 'src', 'components', 'common', 'MobileMenuDrawer.vue'),
    'utf8'
  )
  const siteMenu = readFileSync(
    join(here, '..', 'src', 'components', 'common', 'SiteMenu.vue'),
    'utf8'
  )

  check(
    '接线: AppLayout 的侧边菜单传 agent-name',
    /<site-menu[\s\S]{0,220}?agent-name="agent\?\.name \?\? ''"/.test(layout),
    '不传名字就退回显示 id'
  )
  check(
    '接线: AppLayout 的移动抽屉传 agent-name',
    /<mobile-menu-drawer[\s\S]{0,260}?agent-name="agent\?\.name \?\? ''"/.test(layout)
  )
  check(
    '接线: MobileMenuDrawer 把名字转给 SiteMenu',
    /<site-menu[^>]*agent-name="agentName"/.test(drawer),
    '抽屉里不转传就看不到真实名字'
  )
  check(
    '接线: SiteMenu 把名字交给 buildMenuOptions',
    /buildMenuOptions\(props\.agentId, props\.agentName\)/.test(siteMenu)
  )
  check(
    '接线: SiteMenu 有省略号样式',
    /text-overflow:\s*ellipsis/.test(siteMenu),
    '长名字会撑破侧边栏'
  )
}

/* ───────── 8. 名字来源按 id 匹配（防 stale） ───────── */

{
  const hook = readFileSync(join(here, '..', 'src', 'composables', 'useCurrentAgent.ts'), 'utf8')
  /*
   * agent 必须由 findById(agentId) 推导。若改成「取列表第一个」
   * 或缓存上一次结果，切服务器时就会显示出旧名字。
   */
  check(
    '来源: agent 按 id 从 store 查找',
    /findById\(\s*agentId\.value\s*\)|findById\(agentId\.value\)/.test(hook),
    '必须按当前 id 查，不能用缓存或列表首个'
  )
  check(
    '来源: id 为空时不返回服务器',
    /agentId\.value === null \? undefined/.test(hook)
  )
}

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 菜单服务器名测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 菜单服务器名测试：${passed} 通过，0 失败`)
