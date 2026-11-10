/**
 * CodeEditor 的可复用配置片段（纯 CM6，不含 Vue）
 *
 * 拆出来的目的只有一个：测试要能建出**和组件完全一致**的 EditorState，
 * 直接断言语法树/缩进/只读，而不是断言源码字符串。
 *
 * ## 只读为什么必须走 Compartment
 *
 * `EditorState.readOnly` 是 facet，取值语义是 **some(Boolean)**。
 * 如果只读扩展既出现在基础扩展列表里、又出现在 compartment 里，
 * 一旦基础列表带过 `readOnly.of(true)`，之后无论怎么 reconfigure
 * compartment 都改不掉——因为 base 那份永远贡献一个 true。
 * 所以：**只读/可写只在一个地方配置**（compartment），
 * `buildEditorExtensions` 本身不含只读 facet。
 */
import { EditorState, Compartment } from '@codemirror/state'
import {
  EditorView,
  keymap,
  lineNumbers,
  highlightActiveLine,
  highlightActiveLineGutter,
  drawSelection,
  dropCursor,
  rectangularSelection,
  crosshairCursor
} from '@codemirror/view'
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import {
  defaultHighlightStyle,
  syntaxHighlighting,
  indentOnInput,
  indentUnit,
  bracketMatching
} from '@codemirror/language'
import { closeBrackets, closeBracketsKeymap } from '@codemirror/autocomplete'
import { searchKeymap, highlightSelectionMatches, search } from '@codemirror/search'
import { yaml } from '@codemirror/lang-yaml'

/** YAML 缩进宽度（compose 生态惯例） */
export const INDENT_WIDTH = 2

/** 自动缩进单位；`tabSize` 只管显示宽度，决定缩进几个空格的是 indentUnit */
export const INDENT_UNIT = '  '

/** 只读：禁止编辑，但保留选中与复制 */
export const READONLY_EXT = [EditorState.readOnly.of(true), EditorView.editable.of(false)]

/** 可写 */
export const WRITABLE_EXT = [EditorState.readOnly.of(false), EditorView.editable.of(true)]

/** 主题：全部走项目 CSS 变量，跟其他区域同一套配色 */
export const editorTheme = EditorView.theme({
  '&': {
    fontFamily: 'var(--arkade-mono-font)',
    fontSize: '13px',
    backgroundColor: 'var(--arkade-page-bg)',
    color: 'var(--arkade-text-primary)',
    border: '1px solid var(--arkade-border-color)',
    borderRadius: '4px'
  },
  '.cm-content': {
    caretColor: 'var(--arkade-text-primary)',
    fontFamily: 'var(--arkade-mono-font)'
  },
  '.cm-gutters': {
    backgroundColor: 'transparent',
    color: 'var(--arkade-text-tertiary)',
    border: 'none'
  },
  '.cm-activeLineGutter': { backgroundColor: 'transparent', color: 'var(--arkade-text-secondary)' },
  '.cm-activeLine': { backgroundColor: 'transparent' },
  '&.cm-focused': { outline: 'none', borderColor: 'var(--arkade-link-color, #2080f0)' },
  '.cm-selectionBackground, ::selection': {
    backgroundColor: 'rgba(32, 128, 240, 0.25) !important'
  }
})

/** 键盘：默认 + 搜索 + 历史 + Tab 缩进 */
function editorKeymap() {
  return keymap.of([
    ...closeBracketsKeymap,
    ...defaultKeymap,
    ...searchKeymap,
    ...historyKeymap,
    indentWithTab
  ])
}

export interface EditorConfig {
  ariaLabel?: string
  onUpdate?: (update: { docChanged: boolean; state: EditorState }) => void
}

/**
 * 基础扩展：**不含只读、不含主题**。
 *
 * 那两项由调用方通过 compartment 单独配置，避免同一 facet 被配置两次
 * 导致 reconfigure 失效（见文件头说明）。
 */
export function buildEditorExtensions(opts: EditorConfig = {}) {
  const extensions = [
    lineNumbers(),
    highlightActiveLineGutter(),
    highlightActiveLine(),
    drawSelection(),
    dropCursor(),
    rectangularSelection(),
    crosshairCursor(),
    history(),
    bracketMatching(),
    closeBrackets(),
    indentOnInput(),
    indentUnit.of(INDENT_UNIT),
    EditorState.tabSize.of(INDENT_WIDTH),
    syntaxHighlighting(defaultHighlightStyle, { fallback: true }),
    highlightSelectionMatches(),
    search({ top: true }),
    editorKeymap(),
    yaml(),
    EditorView.lineWrapping,
    EditorView.contentAttributes.of({
      'aria-label': opts.ariaLabel ?? '代码编辑器',
      spellcheck: 'false'
    })
  ]
  if (opts.onUpdate !== undefined) {
    extensions.push(EditorView.updateListener.of(opts.onUpdate))
  }
  return extensions
}

/** 每实例独占的 compartment（共享会让多个编辑器互相改配置） */
export function createCompartments() {
  return { readOnly: new Compartment() }
}
