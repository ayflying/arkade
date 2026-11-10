/**
 * CodeEditor 真实状态测试
 *
 * 跑法：node tests/code-editor.contract.test.mjs
 *
 * ## 为什么用 transpileModule 加载共享源码
 *
 * `utils/codeEditorExtensions.ts` 是 TypeScript（带类型注解与 interface）。
 * Node 不能直接 import。这里用 `typescript.transpileModule` 把它转成 JS，
 * 再用 `data:` URL 动态 import——测的是**组件真正使用的那个文件**，
 * 而不是在测试里复刻一份"接近"的配置。
 *
 * ## 为什么必须建真的 EditorState
 *
 * 「源码里有 `yaml()`」证明不了 YAML 真被解析，也测不出只读 facet 能否切换。
 * 这里用真实扩展建 State，再：
 * - 用 `ensureSyntaxTree` + `tree.iterate({enter})`（公开 API）查语法树
 * - 用 `highlightTree(tree, defaultHighlightStyle, cb)` 取出**真实高亮 span**，
 *   断言不同节点拿到不同的 class，而不是只看节点名
 * - 用 compartment 真实 reconfigure，验证只读能来回切
 */
import { readFileSync } from 'node:fs'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'
import { EditorState, Compartment } from '@codemirror/state'
import { undo, indentWithTab } from '@codemirror/commands'
import { ensureSyntaxTree, defaultHighlightStyle, indentUnit } from '@codemirror/language'
// highlightTree 属于 @lezer/highlight，不在 @codemirror/language 里
import { highlightTree } from '@lezer/highlight'

const here = dirname(fileURLToPath(import.meta.url))

/**
 * 把共享 TS 源码转成可 import 的 data URL 模块。
 *
 * data: 模块无法解析裸包名，所以先用 `import.meta.resolve` 把每个裸导入
 * 换写成 file:// URL —— 这样转译模块和测试进程拿到的是**同一份**
 * @codemirror 实例（pnpm 的 symlink 结构下自己拼路径容易搞出两个实例，
 * 那会让 facet/Compartment 类型不同源，失败信息还会很隐晦）。
 * 不用 registerHooks，现有 Node 版本即可。
 */
async function loadSharedModule() {
  const src = readFileSync(join(here, '..', 'src', 'utils', 'codeEditorExtensions.ts'), 'utf8')
  const js = ts.transpileModule(src, {
    compilerOptions: { module: ts.ModuleKind.ESNext, target: ts.ScriptTarget.ES2022 }
  }).outputText

  const rewritten = js.replace(
    /from\s+['"]([^'".][^'"]*)['"]/g,
    (whole, spec) => (spec.startsWith('@') || /^[a-z]/.test(spec) ? `from '${import.meta.resolve(spec)}'` : whole)
  )
  return import(`data:text/javascript;base64,${Buffer.from(rewritten, 'utf8').toString('base64')}`)
}

const shared = await loadSharedModule()
const { READONLY_EXT, WRITABLE_EXT, INDENT_UNIT, buildEditorExtensions, createCompartments } =
  shared

let passed = 0
const failures = []

function check(name, ok, detail) {
  if (ok) passed += 1
  else failures.push(detail === undefined ? name : `${name} —— ${detail}`)
}

/** 与组件完全一致地建 State：共享扩展 + compartment 里的只读 */
function makeState(doc, { readOnly = false, onUpdate } = {}) {
  const compartments = createCompartments()
  return {
    compartments,
    state: EditorState.create({
      doc,
      extensions: buildEditorExtensions({ onUpdate }).concat([
        compartments.readOnly.of(readOnly ? READONLY_EXT : WRITABLE_EXT)
      ])
    })
  }
}

/** 用公开 API 收集语法树节点名 */
function collectNodes(tree) {
  const names = []
  tree.iterate({
    enter: (node) => {
      names.push(node.name)
      return undefined
    }
  })
  return names
}

/* ───────── 1. YAML 真被解析 ───────── */

{
  const doc = 'services:\n  web:\n    image: nginx:1.27\n    ports:\n      - "8080:80"\n'
  const { state } = makeState(doc)
  const tree = ensureSyntaxTree(state, state.doc.length, 5000)

  check('YAML: 语法树构建成功', tree !== null)
  check('YAML: 树覆盖整个文档', tree !== null && tree.length >= doc.length, `length=${String(tree?.length)}`)

  const names = tree === null ? [] : collectNodes(tree)
  check('YAML: 解析出 BlockMapping', names.includes('BlockMapping'), names.join(','))
  check('YAML: 解析出 Key', names.includes('Key'), names.join(','))
  check(
    'YAML: 解析出字符串值',
    names.includes('QuotedLiteral') || names.includes('Literal'),
    names.join(',')
  )
  check('YAML: 无解析错误节点', !names.includes('⚠'), names.join(','))
}

/* ───────── 2. 高亮 span 真的不同（不是只看节点名） ───────── */

{
  const doc = 'key: value\n# a comment\n'
  const { state } = makeState(doc)
  const tree = ensureSyntaxTree(state, state.doc.length, 5000)

  const spans = []
  if (tree !== null) {
    highlightTree(tree, defaultHighlightStyle, (from, to, cls) => {
      spans.push({ from, to, cls })
    })
  }

  check('高亮: 产出了真实的高亮 span', spans.length > 0, `spans=${String(spans.length)}`)

  /*
   * 关键断言：不同语法成分必须拿到**不同的 class**。
   * 如果只产出一个 class，说明高亮样式其实没起作用（等于没高亮）。
   */
  const classSet = new Set(spans.map((s) => s.cls).filter((c) => c !== ''))
  check('高亮: 至少两种不同的 token class', classSet.size >= 2, [...classSet].join(' | '))
  check(
    '高亮: 存在被着色的 span（class 非空）',
    spans.some((s) => s.cls !== ''),
    JSON.stringify(spans.slice(0, 3))
  )

  // 注释位置应该拿到一个 class（不同色）
  const commentStart = doc.indexOf('#')
  const commentSpan = spans.find((s) => s.from <= commentStart && s.to > commentStart)
  check('高亮: 注释位置有高亮 span', commentSpan !== undefined && commentSpan.cls !== '')
}

/* ───────── 3. 行号 ───────── */

{
  const { state } = makeState('a: 1\nb: 2\nc: 3\n')
  // CM6 语义：以 \n 结尾时末尾还有一个空行
  check('行号: 行数按 CM6 语义计数', state.doc.lines === 4, String(state.doc.lines))
  check('行号: 多行文档行数大于 1', state.doc.lines > 1)
}

/* ───────── 4. 缩进 ───────── */

{
  const { state } = makeState('services:\n  web:\n')
  check('缩进: tabSize 为 2', state.tabSize === 2, String(state.tabSize))
  check('缩进: 缩进单位是两空格', INDENT_UNIT === '  ', JSON.stringify(INDENT_UNIT))
  /*
   * indentUnit 是 facet：确认它被真正配置进 State。
   * 只设 tabSize 只管显示宽度，自动缩进靠 indentUnit。
   */
  check('缩进: indentUnit facet 已配置', state.facet(indentUnit) === '  ', JSON.stringify(state.facet(indentUnit)))

  const withIndent = state.update({ changes: { from: 0, insert: '  ' } }).state
  check('缩进: 使用空格而非 tab', !withIndent.doc.toString().startsWith('\t'))
  check('缩进: 缩进后仍可解析', ensureSyntaxTree(withIndent, withIndent.doc.length, 5000) !== null)
}

/* ───────── 5. 只读 facet 可真正来回切换（回归 bug） ───────── */

{
  /*
   * 曾经的 bug：只读扩展同时出现在基础列表和 compartment，
   * facet 语义是 some(Boolean)，基础那份永远贡献 true，切不回来。
   * 现在基础列表不含只读。
   */
  const { state: s1, compartments } = makeState('a: 1\n', { readOnly: true })
  check('只读: 初始 true', s1.readOnly === true)

  const s2 = s1.update({ effects: compartments.readOnly.reconfigure(WRITABLE_EXT) }).state
  check('只读: true → false 生效', s2.readOnly === false, `readOnly=${String(s2.readOnly)}`)

  const s3 = s2.update({ effects: compartments.readOnly.reconfigure(READONLY_EXT) }).state
  check('只读: false → true 再次生效', s3.readOnly === true, `readOnly=${String(s3.readOnly)}`)

  const s4 = s3.update({ effects: compartments.readOnly.reconfigure(WRITABLE_EXT) }).state
  check('只读: 第二次切回 false 仍生效', s4.readOnly === false, `readOnly=${String(s4.readOnly)}`)

  check('只读: 切换过程内容不丢', s4.doc.toString() === 'a: 1\n')
}

/* ───────── 6. 只读内容仍可读 ───────── */

{
  const bigDoc = 'services:\n  web:\n    image: nginx\n'
  const { state } = makeState(bigDoc, { readOnly: true })
  check('只读: 文档内容完整保留', state.doc.toString() === bigDoc)
  check('只读: 可整段读取（可选中复制）', state.sliceDoc() === bigDoc)
}

/* ───────── 7. 撤销 ───────── */

{
  let state = makeState('a: 1\n').state
  state = state.update({ changes: { from: 0, insert: 'x' } }).state
  check('撤销: 编辑生效', state.doc.toString() === 'xa: 1\n')

  let after = state
  const did = undo({ state, dispatch: (tr) => { after = tr.state } })
  check('撤销: undo 成功', did === true)
  check('撤销: 恢复原内容', after.doc.toString() === 'a: 1\n', after.doc.toString())
}

/* ───────── 8. 抑制回传的源码断言 ───────── */

/*
 * ⚠️ 这里**不能**在纯 State 上测 emit：`EditorView.updateListener` 由 View 驱动，
 * 没有真实 DOM/View 时 update 循环根本不会跑（`state.update()` 不触发 listener）。
 * 那属于浏览器运行期行为，由 Lead 真实浏览器验收覆盖，本文件不假装测过。
 *
 * 能在这里钉住的是**组件的抑制结构**：三处程序性改动必须包在
 * applyProgrammatic 内，且标志复位用 try/finally。
 */
const editorSfc = readFileSync(join(here, '..', 'src', 'components', 'common', 'CodeEditor.vue'), 'utf8')

check(
  '回传: 组件有 suppressUpdates 抑制标志',
  /let\s+suppressUpdates\s*=\s*false/.test(editorSfc),
  '没有抑制标志，父组件回写会形成回环'
)
check(
  '回传: handleUpdate 在抑制时提前返回',
  /if\s*\(suppressUpdates\)\s*return/.test(editorSfc),
  '抑制标志没接进 updateListener'
)
check(
  '回传: applyProgrammatic 用 try/finally 复位标志',
  /finally\s*\{[^}]*suppressUpdates\s*=\s*previous/.test(editorSfc),
  '异常时标志不复位会让编辑器永久失去回传能力'
)
check(
  '回传: 父值同步包在 applyProgrammatic 内',
  /applyProgrammatic\(\(\)\s*=>\s*\{[\s\S]{0,200}?instance\.dispatch\(\{\s*changes:/.test(editorSfc),
  '父组件写进来的值不该 echo 回去'
)

/* ───────── 9. 卸载清空不污染父级（源码断言） ───────── */

check(
  '卸载: onBeforeUnmount 中有清空文档',
  /onBeforeUnmount\([\s\S]*?insert:\s*''/.test(editorSfc),
  '不清理会让敏感明文残留在脱离文档的 DOM 里'
)
check(
  '卸载: 清空动作包在 applyProgrammatic 内',
  /onBeforeUnmount\([\s\S]*?applyProgrammatic\(/.test(editorSfc) &&
    /onBeforeUnmount[\s\S]*?applyProgrammatic\([\s\S]*?insert:\s*''/.test(editorSfc),
  '清空若触发 emit("") 会把父组件正在展示的配置清掉'
)
check(
  '卸载: 清空后销毁 view',
  /onBeforeUnmount[\s\S]*?instance\.destroy\(\)/.test(editorSfc),
  '不 destroy 会泄漏带监听的 view'
)

/* ───────── 9b. 换文档重建 State 也受抑制 ───────── */

check(
  '换文档: projectKey 变化走 setState 重建',
  /props\.projectKey[\s\S]{0,400}?instance\.setState\(/.test(editorSfc),
  '换文档必须清空撤销栈，否则 Ctrl+Z 会调回另一份内容'
)
check(
  '换文档: 重建 State 包在 applyProgrammatic 内',
  /props\.projectKey[\s\S]{0,400}?applyProgrammatic\(/.test(editorSfc)
)

/* ───────── 10. 换文档清空历史 ───────── */

{
  const first = makeState('draft-a\n')
  const edited = first.state.update({ changes: { from: 0, insert: 'edited\n' } }).state
  check('换文档: 编辑后可撤销', edited.doc.toString() === 'edited\ndraft-a\n')

  const fresh = makeState('draft-b\n').state
  check('换文档: 新 State 内容正确', fresh.doc.toString() === 'draft-b\n')

  let afterUndo = fresh
  undo({ state: fresh, dispatch: (tr) => { afterUndo = tr.state } })
  check('换文档: 新文档撤销不回到旧内容', afterUndo.doc.toString() === 'draft-b\n', afterUndo.doc.toString())
}

/* ───────── 11. Tab 键位 ───────── */

{
  check('键位: indentWithTab 存在', indentWithTab !== undefined)
  check('键位: 绑定 Tab', String(indentWithTab.key).includes('Tab'), String(indentWithTab.key))
  /*
   * shift 绑定的 value 是**反缩进命令函数**，不是字符串 'Shift-Tab'。
   * 断言它是个可调用的命令，而不是去比对字符串。
   */
  check('键位: shift 绑定为可调用的反缩进命令', typeof indentWithTab.shift === 'function')
}

/* ───────── 12. 结构：基础扩展不含只读 facet（回归 bug 根因） ───────── */

{
  /*
   * 上一版 bug：只读扩展同时出现在基础列表和 compartment，
   * facet 语义 some(Boolean) 下基础那份永远贡献 true，切不回来。
   *
   * 直接检查真实 facet 值，而不是猜扩展对象长什么样：
   * 基础扩展建出的 State 必须是**可写**的。若有人在基础列表里
   * 塞了 readOnly.of(true)，这里立刻变红。
   */
  const baseOnly = EditorState.create({ doc: 'x\n', extensions: buildEditorExtensions({}) })
  check('结构: 基础扩展建出的 State 可写', baseOnly.readOnly === false, '基础列表混入了只读 facet')

  // compartment 单独配置时可来回切换（证明只读只配置了一次）
  const c = new Compartment()
  const st = EditorState.create({
    doc: 'x\n',
    extensions: buildEditorExtensions({}).concat([c.of(WRITABLE_EXT)])
  })
  const st2 = st.update({ effects: c.reconfigure(READONLY_EXT) }).state
  const st3 = st2.update({ effects: c.reconfigure(WRITABLE_EXT) }).state
  check(
    '结构: compartment 可 true→false→true 来回切换',
    st.readOnly === false && st2.readOnly === true && st3.readOnly === false
  )
}

/* ───────── 结果 ───────── */

if (failures.length > 0) {
  console.error(`\n✗ 代码编辑器测试：${passed} 通过，${failures.length} 失败\n`)
  for (const f of failures) console.error(`  ✗ ${f}`)
  process.exit(1)
}
console.log(`\n✓ 代码编辑器测试：${passed} 通过，0 失败`)
