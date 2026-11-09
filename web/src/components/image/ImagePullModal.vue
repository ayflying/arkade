<script setup lang="ts">
/**
 * 拉取镜像弹窗
 *
 * 交互刻意简单：填镜像引用 → 提交 → 显示任务进度。
 * 不提供「选择仓库/标签」的分步表单——用户通常是从别处复制
 * `nginx:1.27` 这样一整串过来，分步只会更慢。
 *
 * ## 不支持私有仓库凭据
 *
 * 后端不支持私有仓库登录，所以这里不能说「需要登录的镜像也能拉」。
 * 输入框下的说明直接写清楚，避免用户拿私有镜像试很久才失败。
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { NAlert, NButton, NFormItem, NInput, NModal, NProgress } from 'naive-ui'
import { pullImage } from '@/api/imageTasks'
import type { ImageTask } from '@/types/imageTask'
import { pollTaskUntilFinished } from '@/composables/useImageTasks'
import { formatBytes } from '@/utils/formatSize'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'

interface Props {
  show: boolean
  agentId: string
  /** 服务器是否在线：离线时后端必然失败，先禁用并说明 */
  online: boolean
}

const props = defineProps<Props>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  /** 任务已提交，交给页面插进任务列表 */
  submitted: [task: ImageTask]
}>()

const reference = ref('')
const submitting = ref(false)
const errorText = ref('')
/** 已提交的任务（提交后进入进度态） */
const task = ref<ImageTask | null>(null)
/** 轮询是否已放弃（组件卸载/超时） */
let disposed = false

const canSubmit = computed(
  () => props.online && !submitting.value && reference.value.trim() !== ''
)

/** 校验镜像引用：只做明显的格式拦截，真正的合法性由后端判定 */
function validateReference(value: string): string | null {
  const v = value.trim()
  if (v === '') return '请填写镜像名，例如 nginx:1.27'
  // 空白字符和换行会让 docker 直接报错，这里提前拦下并给出可读原因
  if (/\s/.test(v)) return '镜像名里不能有空格或换行'
  return null
}

async function handleSubmit(): Promise<void> {
  if (!canSubmit.value) return
  const invalid = validateReference(reference.value)
  if (invalid !== null) {
    errorText.value = invalid
    return
  }

  errorText.value = ''
  submitting.value = true
  try {
    const res = await pullImage(props.agentId, { reference: reference.value.trim() })
    // 202 = 已接受，不是已完成。把任务交给上层插入列表，并在弹窗内继续显示进度。
    task.value = res.task
    emit('submitted', res.task)
    void follow(res.task.id)
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : '拉取失败'
    toastError(errorText.value)
  } finally {
    submitting.value = false
  }
}

/**
 * 跟踪任务直到结束。
 *
 * 这里只负责把最新状态写进 `task` 给弹窗内的进度条用；
 * 列表里的那份由页面的轮询刷新，两边不冲突（都只读）。
 */
async function follow(taskId: string): Promise<void> {
  try {
    const final = await pollTaskUntilFinished(taskId, (t) => {
      if (!disposed) task.value = t
    })
    if (disposed || final === null) return
    if (final.status === 'succeeded') {
      toastSuccess('镜像拉取完成')
    } else if (final.status === 'failed') {
      toastWarning(final.message === '' ? '镜像拉取失败' : final.message)
    } else if (final.status === 'canceled') {
      toastWarning('任务已取消。已下载的层不会回滚。')
    }
  } catch {
    // 轮询失败不打断用户：列表页自己的轮询会继续尝试，这里不再弹错
  }
}

/** 关闭：提交中不允许关（避免用户以为取消了），有任务时允许离开（列表里还有） */
function handleClose(): void {
  if (submitting.value) return
  reference.value = ''
  errorText.value = ''
  task.value = null
  emit('update:show', false)
}

watch(
  () => props.show,
  (show) => {
    if (show) {
      reference.value = ''
      errorText.value = ''
      task.value = null
      disposed = false
    }
  }
)

onUnmounted(() => {
  disposed = true
})

/** 进度文案：progress 为 null 就是「未知」，不显示 0% */
const progressText = computed(() => {
  const t = task.value
  if (t === null) return ''
  if (t.progress !== null) {
    return `${String(Math.round(t.progress * 100))}%`
  }
  if (t.bytes_total !== null && t.bytes_total > 0) {
    return `${formatBytes(t.bytes_done)} / ${formatBytes(t.bytes_total)}`
  }
  if (t.bytes_done > 0) return formatBytes(t.bytes_done)
  return '进度未知'
})
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    title="拉取镜像"
    :style="{ maxWidth: '560px', width: '94vw' }"
    :bordered="false"
    :mask-closable="false"
    @update:show="(v: boolean) => { if (!v) handleClose() }"
  >
    <n-alert v-if="!online" type="warning" :bordered="false" class="pull__alert">
      这台服务器当前离线，无法拉取镜像。请等服务器上线后再试。
    </n-alert>

    <n-form-item label="镜像名" :show-feedback="false">
      <n-input
        v-model:value="reference"
        placeholder="例如 nginx:1.27 或 ghcr.io/foo/bar:latest"
        :disabled="submitting || task !== null"
        @keyup.enter="handleSubmit"
      />
    </n-form-item>
    <p class="pull__hint">
      拉取到<strong>当前这台服务器</strong>。不支持私有仓库凭据，需要登录的镜像会拉取失败。
    </p>

    <!-- 提交后的进度区 -->
    <div v-if="task !== null" class="pull__progress">
      <div class="pull__progress-head">
        <span class="pull__progress-phase">{{ task.phase === '' ? '处理中' : task.phase }}</span>
        <span class="pull__progress-text">{{ progressText }}</span>
      </div>
      <!--
        progress 为 null 时用不确定进度条：
        它表达「在做，但不知道还剩多少」，比一个假的 0% 诚实。
      -->
      <n-progress
        v-if="task.progress !== null"
        type="line"
        :percentage="Math.min(100, Math.max(0, Math.round(task.progress * 100)))"
        :show-indicator="false"
        :height="10"
      />
      <n-progress v-else type="line" :percentage="0" processing :show-indicator="false" :height="10" />
      <p v-if="task.message !== ''" class="pull__message">{{ task.message }}</p>
    </div>

    <p v-if="errorText !== ''" class="pull__error">{{ errorText }}</p>

    <template #footer>
      <div class="pull__footer">
        <n-button size="small" :disabled="submitting" @click="handleClose">
          {{ task !== null ? '关闭' : '取消' }}
        </n-button>
        <n-button
          size="small"
          type="primary"
          :loading="submitting"
          :disabled="!canSubmit || task !== null"
          @click="handleSubmit"
        >
          开始拉取
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.pull__alert {
  margin-bottom: 12px;
}

.pull__hint {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--arkade-text-tertiary);
}

.pull__progress {
  margin-top: 14px;
  padding: 10px 12px;
  border: 1px solid var(--arkade-border-color);
  border-radius: 6px;
}

.pull__progress-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
}

.pull__progress-phase {
  font-size: 12px;
  color: var(--arkade-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.pull__progress-text {
  font-size: 12px;
  white-space: nowrap;
  color: var(--arkade-text-tertiary);
}

.pull__message {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--arkade-text-secondary);
}

.pull__error {
  margin: 10px 0 0;
  font-size: 12px;
  color: #d03050;
}

.pull__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
