<script setup lang="ts">
/**
 * 重命名弹窗
 *
 * 必须说明「目标已存在会被拒绝」：用户很容易以为重命名是「覆盖式改名」，
 * 从而在目标已存在时得到一个意外的失败。提前讲清比事后解释好。
 */
import { ref, watch } from 'vue'
import { NButton, NInput, NModal } from 'naive-ui'

interface Props {
  show: boolean
  /** 原名称，打开时预填 */
  initialName: string
  loading: boolean
  errorText: string
}

const props = defineProps<Props>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  submit: [name: string]
}>()

const name = ref('')
const localError = ref('')

function validate(value: string): string | null {
  const v = value.trim()
  if (v === '') return '请输入新名称'
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

watch(
  () => props.show,
  (show) => {
    if (show) {
      name.value = props.initialName
      localError.value = ''
    }
  }
)
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    title="重命名"
    :style="{ maxWidth: '420px', width: '92vw' }"
    :bordered="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <n-input v-model:value="name" placeholder="新名称" :disabled="loading" @keyup.enter="handleSubmit" />
    <p class="hint">如果目标名称已存在，操作会被拒绝，不会覆盖已有条目。</p>
    <p v-if="localError !== '' || errorText !== ''" class="error">
      {{ localError !== '' ? localError : errorText }}
    </p>

    <template #footer>
      <div class="footer">
        <n-button size="small" :disabled="loading" @click="emit('update:show', false)">取消</n-button>
        <n-button size="small" type="primary" :loading="loading" @click="handleSubmit">确定</n-button>
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
