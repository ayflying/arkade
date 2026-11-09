<script setup lang="ts">
/**
 * 新建文件 / 新建文件夹 弹窗（两者结构相同，合并成一个组件）
 *
 * 合并而不是写两份：字段（一个名称输入）、校验（路径分隔符 / . / ..）、
 * 交互（取消+确定）完全一致，分开写迟早改一处忘另一处。
 *
 * `createOnly` 的说明只在建文件时显示——建目录没有「覆盖」这回事，
 * 多一句会让用户以为有什么风险。
 */
import { computed, ref, watch } from 'vue'
import { NButton, NInput, NModal } from 'naive-ui'

interface Props {
  show: boolean
  /** 'file' 建空文件；'dir' 建文件夹 */
  kind: 'file' | 'dir'
  loading: boolean
  /** 后端返回的错误文案 */
  errorText: string
}

const props = defineProps<Props>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  submit: [name: string]
}>()

const name = ref('')
/** 本地校验错误 */
const localError = ref('')

const title = computed(() => (props.kind === 'file' ? '新建文件' : '新建文件夹'))
const placeholder = computed(() =>
  props.kind === 'file' ? '文件名，例如 config.yml' : '文件夹名'
)

/** 名称不能含路径分隔符，也不能是 . / .. */
function validate(value: string): string | null {
  const v = value.trim()
  if (v === '') return '请输入名称'
  if (v.includes('/') || v.includes('\\')) return '名称里不能有路径分隔符'
  if (v === '.' || v === '..') return '不能使用 . 或 ..'
  return null
}

function handleSubmit(): void {
  const invalid = validate(name.value)
  if (invalid !== null) {
    localError.value = invalid
    return
  }
  localError.value = ''
  emit('submit', name.value.trim())
}

// 打开时清空，避免复用上一次的输入
watch(
  () => props.show,
  (show) => {
    if (show) {
      name.value = ''
      localError.value = ''
    }
  }
)

/** 显示的错误：本地校验优先于后端错误 */
const shownError = computed(() => (localError.value !== '' ? localError.value : props.errorText))
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    :title="title"
    :style="{ maxWidth: '420px', width: '92vw' }"
    :bordered="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <n-input
      v-model:value="name"
      :placeholder="placeholder"
      :disabled="loading"
      @keyup.enter="handleSubmit"
    />
    <p v-if="kind === 'file'" class="hint">
      会创建一个空文件。如果同名文件已存在，操作会被拒绝，不会覆盖。
    </p>
    <p v-if="shownError !== ''" class="error">{{ shownError }}</p>

    <template #footer>
      <div class="footer">
        <n-button size="small" :disabled="loading" @click="emit('update:show', false)">取消</n-button>
        <n-button size="small" type="primary" :loading="loading" @click="handleSubmit">
          创建
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.hint {
  margin: 8px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--arkade-text-tertiary);
}

.error {
  margin: 8px 0 0;
  font-size: 12px;
  color: #d03050;
}

.footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
