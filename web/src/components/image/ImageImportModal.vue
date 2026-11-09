<script setup lang="ts">
/**
 * 导入镜像弹窗（上传 tar 文件）
 *
 * ## 上传与导入是两个阶段，界面必须分开说
 *
 * 1. **上传阶段**：浏览器把文件发到 Hub，百分比来自 XHR 真实已发送字节；
 * 2. **导入阶段**：Hub/Agent 解包并写入镜像库，这一段的进度来自任务轮询。
 *
 * 上传到 100% 只是「文件传完了」，此时镜像还不可用。
 * 如果把两者混成一个进度条，用户会以为 100% 就等于导入成功。
 *
 * ## 大文件约束
 *
 * - 2 GiB 上限，选文件时立即校验（不是传完才被拒）；
 * - 上传用原始 `application/x-tar`，不是 multipart，也不是 base64；
 * - 全程不把文件读进内存（不 FileReader、不 base64），直接把 File 交给 XHR。
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { NAlert, NButton, NModal, NProgress } from 'naive-ui'
import { IMPORT_MAX_BYTES, importImage } from '@/api/imageTasks'
import type { ImageTask } from '@/types/imageTask'
import { pollTaskUntilFinished } from '@/composables/useImageTasks'
import { formatBytes } from '@/utils/formatSize'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'

interface Props {
  show: boolean
  agentId: string
  online: boolean
}

const props = defineProps<Props>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  submitted: [task: ImageTask]
}>()

/** 阶段：选择文件 → 上传中 → 导入中(轮询) → 结束 */
type Phase = 'pick' | 'uploading' | 'importing' | 'done'

const phase = ref<Phase>('pick')
const file = ref<File | null>(null)
const errorText = ref('')
/** 上传进度（真实已发送字节） */
const uploadedBytes = ref(0)
const uploadTotalBytes = ref(0)
/** 提交后拿到的任务 */
const task = ref<ImageTask | null>(null)
/** 用于取消上传 */
let abortController: AbortController | null = null
let disposed = false

const fileInputRef = ref<HTMLInputElement | null>(null)

const canSubmit = computed(
  () => props.online && file.value !== null && phase.value === 'pick'
)

/** 上传百分比：只在上传阶段有意义 */
const uploadPercent = computed(() => {
  if (uploadTotalBytes.value <= 0) return 0
  return Math.min(100, Math.round((uploadedBytes.value / uploadTotalBytes.value) * 100))
})

/** 选择文件后立即校验大小，不等到传完才报错 */
function handleFileChange(event: Event): void {
  const input = event.target as HTMLInputElement
  const picked = input.files?.[0] ?? null
  errorText.value = ''

  if (picked === null) {
    file.value = null
    return
  }
  if (picked.size > IMPORT_MAX_BYTES) {
    errorText.value = `文件大小 ${formatBytes(picked.size)} 超过上限 ${formatBytes(IMPORT_MAX_BYTES)}，请先在服务器上拆分或压缩后再导入。`
    file.value = null
    // 清空 input，避免用户以为文件已选上
    input.value = ''
    return
  }
  if (picked.size === 0) {
    errorText.value = '这个文件是空的，无法导入。'
    file.value = null
    input.value = ''
    return
  }
  file.value = picked
}

/** 打开系统文件选择框 */
function pickFile(): void {
  fileInputRef.value?.click()
}

async function handleSubmit(): Promise<void> {
  if (!canSubmit.value || file.value === null) return
  errorText.value = ''
  phase.value = 'uploading'
  uploadedBytes.value = 0
  uploadTotalBytes.value = file.value.size

  abortController = new AbortController()
  try {
    const res = await importImage(props.agentId, file.value, {
      signal: abortController.signal,
      onProgress: (loaded, total) => {
        uploadedBytes.value = loaded
        uploadTotalBytes.value = total
      }
    })
    // 上传完成，进入服务端导入阶段：进度改由任务轮询提供
    task.value = res.task
    phase.value = 'importing'
    emit('submitted', res.task)
    void follow(res.task.id)
  } catch (err) {
    phase.value = 'pick'
    if (err instanceof DOMException && err.name === 'AbortError') {
      errorText.value = '上传已取消'
      return
    }
    errorText.value = err instanceof Error ? err.message : '上传失败'
    toastError(errorText.value)
  } finally {
    abortController = null
  }
}

/** 取消上传（仅在文件还没传完时可用） */
function handleCancelUpload(): void {
  abortController?.abort()
}

/** 跟踪服务端导入进度 */
async function follow(taskId: string): Promise<void> {
  try {
    const final = await pollTaskUntilFinished(taskId, (t) => {
      if (!disposed) task.value = t
    })
    if (disposed || final === null) return
    phase.value = 'done'
    if (final.status === 'succeeded') {
      toastSuccess('镜像导入完成')
    } else if (final.status === 'failed') {
      toastWarning(final.message === '' ? '镜像导入失败' : final.message)
    } else if (final.status === 'canceled') {
      toastWarning('导入已取消。已写入的部分不会回滚，可能已更新同名标签。')
    }
  } catch {
    // 轮询失败交给列表页自己的轮询继续尝试
  }
}

function resetState(): void {
  phase.value = 'pick'
  file.value = null
  errorText.value = ''
  uploadedBytes.value = 0
  uploadTotalBytes.value = 0
  task.value = null
}

function handleClose(): void {
  // 上传中不许关（关掉只是看不见，请求还在跑）
  if (phase.value === 'uploading') return
  resetState()
  emit('update:show', false)
}

watch(
  () => props.show,
  (show) => {
    if (show) {
      resetState()
      disposed = false
    }
  }
)

onUnmounted(() => {
  disposed = true
  // 组件销毁兜底：中断未完成的上传，别让请求在后台空跑
  abortController?.abort()
})
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    title="导入镜像"
    :style="{ maxWidth: '600px', width: '94vw' }"
    :bordered="false"
    :mask-closable="false"
    @update:show="(v: boolean) => { if (!v) handleClose() }"
  >
    <n-alert v-if="!online" type="warning" :bordered="false" class="import__alert">
      这台服务器当前离线，无法导入镜像。请等服务器上线后再试。
    </n-alert>

    <!-- 选择文件：用隐藏 input + 按钮，避免原生 input 的丑陋样式 -->
    <input
      ref="fileInputRef"
      type="file"
      accept=".tar,application/x-tar"
      class="import__file-input"
      @change="handleFileChange"
    />

    <div v-if="phase === 'pick'" class="import__picker">
      <n-button size="small" @click="pickFile">选择 tar 文件</n-button>
      <span v-if="file !== null" class="import__filename" :title="file.name">
        {{ file.name }}（{{ formatBytes(file.size) }}）
      </span>
      <span v-else class="import__placeholder">未选择文件</span>
    </div>

    <p class="import__hint">
      镜像文件上限 <strong>{{ formatBytes(IMPORT_MAX_BYTES) }}</strong>，格式为
      <code>docker save</code> 导出的 <code>.tar</code>。
      导入会写入<strong>当前这台服务器</strong>的镜像库；若已存在同名标签会被更新。
    </p>

    <!-- 上传阶段：真实字节进度 -->
    <div v-if="phase === 'uploading'" class="import__progress">
      <div class="import__progress-head">
        <span>正在上传文件</span>
        <span class="import__progress-text">
          {{ uploadPercent }}% · {{ formatBytes(uploadedBytes) }} / {{ formatBytes(uploadTotalBytes) }}
        </span>
      </div>
      <n-progress type="line" :percentage="uploadPercent" :show-indicator="false" :height="10" />
    </div>

    <!-- 导入阶段：上传已完成，进度来自任务轮询 -->
    <div v-if="phase === 'importing' && task !== null" class="import__progress">
      <div class="import__progress-head">
        <span>{{ task.phase === '' ? '正在导入' : task.phase }}</span>
        <span class="import__progress-text">
          {{ task.progress !== null ? `${String(Math.round(task.progress * 100))}%` : '进度未知' }}
        </span>
      </div>
      <n-progress
        v-if="task.progress !== null"
        type="line"
        :percentage="Math.min(100, Math.max(0, Math.round(task.progress * 100)))"
        :show-indicator="false"
        :height="10"
      />
      <!--
        进度未知时用不确定进度条。
        注意这里**不能**继续显示上传阶段那个 100%——文件传完不代表导入完。
      -->
      <n-progress v-else type="line" :percentage="0" processing :show-indicator="false" :height="10" />
      <p class="import__phase-note">
        文件已上传，正在服务器上写入镜像。此阶段耗时取决于文件大小，请勿关闭页面。
      </p>
      <p v-if="task.message !== ''" class="import__message">{{ task.message }}</p>
    </div>

    <div v-if="phase === 'done' && task !== null" class="import__progress">
      <div class="import__progress-head">
        <span>{{ task.phase === '' ? '已结束' : task.phase }}</span>
      </div>
      <p v-if="task.message !== ''" class="import__message">{{ task.message }}</p>
    </div>

    <p v-if="errorText !== ''" class="import__error">{{ errorText }}</p>

    <template #footer>
      <div class="import__footer">
        <n-button size="small" :disabled="phase === 'uploading'" @click="handleClose">
          关闭
        </n-button>
        <n-button
          v-if="phase === 'uploading'"
          size="small"
          type="warning"
          @click="handleCancelUpload"
        >
          取消上传
        </n-button>
        <n-button
          v-else
          size="small"
          type="primary"
          :disabled="!canSubmit"
          @click="handleSubmit"
        >
          开始导入
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.import__alert {
  margin-bottom: 12px;
}

/* 隐藏原生 input，但保留可访问性（按钮点击触发） */
.import__file-input {
  display: none;
}

.import__picker {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.import__filename {
  font-size: 13px;
  color: var(--arkade-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 100%;
}

.import__placeholder {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.import__hint {
  margin: 10px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--arkade-text-tertiary);
}

.import__progress {
  margin-top: 14px;
  padding: 10px 12px;
  border: 1px solid var(--arkade-border-color);
  border-radius: 6px;
}

.import__progress-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
  font-size: 12px;
  color: var(--arkade-text-secondary);
}

.import__progress-text {
  white-space: nowrap;
  color: var(--arkade-text-tertiary);
}

.import__phase-note {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.import__message {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--arkade-text-secondary);
}

.import__error {
  margin: 10px 0 0;
  font-size: 12px;
  color: #d03050;
}

.import__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
