<script setup lang="ts">
/**
 * 共享代码编辑器（CodeMirror 6）
 *
 * 扩展配置在 `utils/codeEditorExtensions.ts`，这里只管
 * 生命周期 + v-model 同步 + 只读热切换。
 *
 * ## 三个必须守住的点
 *
 * 1. **卸载清空不能回传**：compose 配置含明文密钥，卸载时要清掉残留 DOM 内容，
 *    但那次 dispatch 必须抑制 emit——否则 `emit('')` 会把父组件正在展示的配置清空。
 * 2. **父组件回写不能 echo**：程序性改动不是"用户输入"，同样要抑制。
 * 3. **只读 ≠ 禁用**：只读时仍要能滚动、选中、复制，所以用
 *    `EditorState.readOnly` + `editable.of(false)`，不是换成 `<pre>`。
 */
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import { EditorState } from '@codemirror/state'
import { EditorView } from '@codemirror/view'
import { undo, redo } from '@codemirror/commands'
import {
  READONLY_EXT,
  WRITABLE_EXT,
  buildEditorExtensions,
  createCompartments,
  editorTheme
} from '@/utils/codeEditorExtensions'

interface Props {
  /** 编辑器内容（受控） */
  modelValue: string
  /** 只读：禁止编辑，但仍可选中复制 */
  disabled?: boolean
  /** 无障碍名称 */
  ariaLabel?: string
  minHeight?: number
  maxHeight?: number
  /**
   * 文档身份：变化表示换了一份文档，撤销栈必须清空。
   *
   * 典型场景是同一编排里「原始配置 ↔ 草稿」切换——那是两份内容，
   * 留着旧历史会导致 Ctrl+Z 把另一份内容调回来。
   * 同一文档内的编辑不走这里，光标与历史都保住。
   */
  projectKey?: string
}

const props = withDefaults(defineProps<Props>(), {
  disabled: false,
  ariaLabel: '代码编辑器',
  minHeight: 340,
  maxHeight: 560
})

const emit = defineEmits<{ (e: 'update:modelValue', value: string): void }>()

const host = ref<HTMLElement | null>(null)
/** CM6 view 内部状态量大且不可观测，用 shallowRef 避免深代理 */
const view = shallowRef<EditorView | null>(null)
const compartments = createCompartments()

/** 已同步过的内容，用来打破回写循环 */
let syncedValue = ''

/** 抑制回传：程序性改动不能触发 update:modelValue */
let suppressUpdates = false

function handleUpdate(update: { docChanged: boolean; state: EditorState }): void {
  if (!update.docChanged) return
  const text = update.state.doc.toString()
  syncedValue = text
  if (suppressUpdates) return
  emit('update:modelValue', text)
}

/** 以程序性身份执行，期间不 emit；try/finally 保证标志复位 */
function applyProgrammatic(fn: () => void): void {
  const previous = suppressUpdates
  suppressUpdates = true
  try {
    fn()
  } finally {
    suppressUpdates = previous
  }
}

/** 建完整 State；updateListener 必须建 State 时挂上，事后 setState 会清空历史 */
function createState(): EditorState {
  return EditorState.create({
    doc: props.modelValue,
    extensions: buildEditorExtensions({
      ariaLabel: props.ariaLabel,
      onUpdate: handleUpdate
    }).concat([
      // 只读与主题**只在这里**配置，不在 buildEditorExtensions 里重复
      compartments.readOnly.of(props.disabled ? READONLY_EXT : WRITABLE_EXT),
      editorTheme
    ])
  })
}

function applySize(instance: EditorView): void {
  instance.dom.style.minHeight = `${String(props.minHeight)}px`
  instance.dom.style.maxHeight = `${String(props.maxHeight)}px`
  instance.dom.style.overflow = 'auto'
}

onMounted(() => {
  if (host.value === null) return
  syncedValue = props.modelValue
  const instance = new EditorView({ state: createState(), parent: host.value })
  applySize(instance)
  view.value = instance
})

onBeforeUnmount(() => {
  const instance = view.value
  if (instance === null) return
  // 清残留内容（含明文密钥），期间抑制 emit，避免清空父组件状态
  applyProgrammatic(() => {
    try {
      instance.dispatch({ changes: { from: 0, to: instance.state.doc.length, insert: '' } })
    } catch {
      // 销毁期 State 可能已不可用
    }
  })
  instance.destroy()
  view.value = null
  syncedValue = ''
})

/**
 * 内容同步：只在真的不同时做最小替换（不是 setState），
 * 保住光标与撤销栈；期间抑制 emit 防止 echo 回父组件。
 */
watch(
  () => props.modelValue,
  (next) => {
    const instance = view.value
    if (instance === null || next === syncedValue) return
    syncedValue = next
    const current = instance.state.doc.toString()
    if (next === current) return
    applyProgrammatic(() => {
      instance.dispatch({ changes: { from: 0, to: current.length, insert: next } })
    })
  }
)

/** 只读热切换：reconfigure 保留文档、光标与撤销历史 */
watch(
  () => props.disabled,
  (disabled) => {
    view.value?.dispatch({
      effects: compartments.readOnly.reconfigure(disabled ? READONLY_EXT : WRITABLE_EXT)
    })
  }
)

/** 换文档才重建 State（唯一允许清空撤销栈的地方） */
watch(
  () => props.projectKey,
  () => {
    const instance = view.value
    if (instance === null) return
    syncedValue = props.modelValue
    applyProgrammatic(() => {
      instance.setState(createState())
    })
  }
)

/** 尺寸变更 */
watch(
  () => [props.minHeight, props.maxHeight] as const,
  () => {
    const instance = view.value
    if (instance !== null) applySize(instance)
  }
)

defineExpose({
  focus: (): void => {
    view.value?.focus()
  },
  undo: (): boolean => (view.value === null ? false : undo(view.value)),
  redo: (): boolean => (view.value === null ? false : redo(view.value))
})
</script>

<template>
  <!-- 无障碍名通过 contentAttributes 挂在 .cm-content 上，不在这个 div 上 -->
  <div ref="host" class="code-editor" :class="{ 'code-editor--readonly': props.disabled }" />
</template>

<style scoped>
.code-editor {
  width: 100%;
  overflow: hidden;
}

/* 只读时轻微降透明度：一眼看出不可编辑，同时保持可读可选中 */
.code-editor--readonly {
  opacity: 0.92;
}

.code-editor :deep(.cm-editor) {
  height: 100%;
}

.code-editor :deep(.cm-scroller) {
  font-family: var(--arkade-mono-font);
  line-height: 1.6;
}
</style>
