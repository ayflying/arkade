<script setup lang="ts">
/**
 * 迁移镜像弹窗（跨服务器复制镜像）
 *
 * ## 语义必须讲清（用户最容易误解的三个点）
 *
 * 1. **不启动容器**：只是把镜像复制过去，目标上不会自动跑起来；
 * 2. **不删源**：源服务器上的镜像原样保留；
 * 3. **可能更新目标上的同名标签**：目标已有同名 tag 会被覆盖。
 *    这不是「新增一个」，而是「替换那个标签指向的镜像」，
 *    依赖旧镜像的容器下次重建就会换镜像。
 *
 * ## 目标服务器选择
 *
 * 用现有 agents store 的列表，不额外请求、不猜用户权限。
 * 同源禁用（迁移到自己没有意义），离线目标也给提示但仍可选——
 * 后端会拒绝，但先让用户看到「它离线」比点了才报错好。
 */
import { computed, onUnmounted, ref, watch } from 'vue'
import { NAlert, NButton, NModal, NProgress, NSelect } from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { migrateImage } from '@/api/imageTasks'
import type { ImageTask } from '@/types/imageTask'
import { pollTaskUntilFinished } from '@/composables/useImageTasks'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'
import { formatBytes } from '@/utils/formatSize'

interface AgentOption {
  id: string
  name: string
  online: boolean
}

interface Props {
  show: boolean
  /** 源服务器 id（当前页面所在的服务器） */
  agentId: string
  /** 要迁移的镜像引用 */
  reference: string
  /** 镜像大小，用于提示传输量；未知时传 null */
  sizeBytes?: number | null
  /** 全部服务器（含源），用于渲染目标下拉 */
  agents: AgentOption[]
  online: boolean
}

const props = withDefaults(defineProps<Props>(), {
  sizeBytes: null
})

const emit = defineEmits<{
  'update:show': [value: boolean]
  submitted: [task: ImageTask]
}>()

const targetAgentId = ref<string>('')
const submitting = ref(false)
const errorText = ref('')
const task = ref<ImageTask | null>(null)
let disposed = false

/** 目标候选：排除源服务器本身（迁移到自己没有意义） */
const options = computed<SelectOption[]>(() =>
  props.agents
    .filter((a) => a.id !== props.agentId)
    .map((a) => ({
      label: a.online ? a.name : `${a.name}（离线）`,
      value: a.id,
      // NSelect 支持 disabled
      disabled: !a.online
    }))
)

/** 选中的目标服务器名，用于确认文案 */
const canSubmit = computed(
  () => props.online && !submitting.value && targetAgentId.value !== '' && task.value === null
)

/** 源与目标是否相同（防御：下拉已排除，但状态可能被外部改） */
const sameAgent = computed(() => targetAgentId.value === props.agentId)

async function handleSubmit(): Promise<void> {
  if (!canSubmit.value) return
  if (sameAgent.value) {
    errorText.value = '目标服务器不能与源服务器相同'
    return
  }

  errorText.value = ''
  submitting.value = true
  try {
    const res = await migrateImage(props.agentId, {
      reference: props.reference,
      target_agent_id: targetAgentId.value,
      // 硬性确认位：后端要求 true 才执行
      confirmation: true
    })
    task.value = res.task
    emit('submitted', res.task)
    void follow(res.task.id)
  } catch (err) {
    errorText.value = err instanceof Error ? err.message : '迁移失败'
    toastError(errorText.value)
  } finally {
    submitting.value = false
  }
}

async function follow(taskId: string): Promise<void> {
  try {
    const final = await pollTaskUntilFinished(taskId, (t) => {
      if (!disposed) task.value = t
    })
    if (disposed || final === null) return
    if (final.status === 'succeeded') {
      toastSuccess('镜像已复制到目标服务器（未启动容器，源镜像未删除）')
    } else if (final.status === 'failed') {
      toastWarning(final.message === '' ? '镜像迁移失败' : final.message)
    } else if (final.status === 'canceled') {
      toastWarning('迁移已取消。目标服务器上已写入的部分不会回滚。')
    }
  } catch {
    // 交给列表轮询继续
  }
}

function resetState(): void {
  targetAgentId.value = ''
  submitting.value = false
  errorText.value = ''
  task.value = null
}

function handleClose(): void {
  if (submitting.value) return
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
})
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    title="迁移镜像到其他服务器"
    :style="{ maxWidth: '600px', width: '94vw' }"
    :bordered="false"
    :mask-closable="false"
    @update:show="(v: boolean) => { if (!v) handleClose() }"
  >
    <n-alert v-if="!online" type="warning" :bordered="false" class="migrate__alert">
      源服务器当前离线，无法迁移镜像。请等服务器上线后再试。
    </n-alert>

    <div class="migrate__row">
      <span class="migrate__label">源镜像</span>
      <span class="migrate__value" :title="reference">{{ reference }}</span>
    </div>
    <div v-if="sizeBytes !== null && sizeBytes > 0" class="migrate__row">
      <span class="migrate__label">大小</span>
      <span class="migrate__value">{{ formatBytes(sizeBytes) }}</span>
    </div>

    <div class="migrate__row">
      <span class="migrate__label">目标服务器</span>
      <n-select
        v-model:value="targetAgentId"
        class="migrate__select"
        :options="options"
        placeholder="选择要复制到哪台服务器"
        :disabled="submitting || task !== null"
        filterable
      />
    </div>

    <!-- 后果说明：迁移不是「移动」，也不启动任何东西 -->
    <n-alert type="info" :bordered="false" class="migrate__notice">
      迁移会把这台服务器上的镜像<strong>复制</strong>到目标服务器：
      <br />
      · <strong>不会删除源镜像</strong>；
      <br />
      · <strong>不会在目标上启动任何容器</strong>；
      <br />
      · 目标服务器上<strong>已存在同名标签时会被更新</strong>，依赖旧镜像的容器下次重建会用到新镜像。
    </n-alert>

    <!-- 提交后的进度 -->
    <div v-if="task !== null" class="migrate__progress">
      <div class="migrate__progress-head">
        <span>{{ task.phase === '' ? '处理中' : task.phase }}</span>
        <span class="migrate__progress-text">
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
      <n-progress v-else type="line" :percentage="0" processing :show-indicator="false" :height="10" />
      <p v-if="task.message !== ''" class="migrate__message">{{ task.message }}</p>
    </div>

    <p v-if="errorText !== ''" class="migrate__error">{{ errorText }}</p>

    <template #footer>
      <div class="migrate__footer">
        <n-button size="small" :disabled="submitting" @click="handleClose">
          {{ task !== null ? '关闭' : '取消' }}
        </n-button>
        <n-button
          size="small"
          type="primary"
          :loading="submitting"
          :disabled="!canSubmit"
          @click="handleSubmit"
        >
          开始迁移
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.migrate__alert {
  margin-bottom: 12px;
}

.migrate__row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 10px;
}

.migrate__label {
  flex: 0 0 72px;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.migrate__value {
  font-size: 13px;
  color: var(--arkade-text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-family: var(--arkade-mono-font);
}

.migrate__select {
  flex: 1 1 auto;
  min-width: 0;
}

.migrate__notice {
  margin-top: 6px;
  font-size: 12px;
  line-height: 1.7;
}

.migrate__progress {
  margin-top: 14px;
  padding: 10px 12px;
  border: 1px solid var(--arkade-border-color);
  border-radius: 6px;
}

.migrate__progress-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 8px;
  font-size: 12px;
  color: var(--arkade-text-secondary);
}

.migrate__progress-text {
  white-space: nowrap;
  color: var(--arkade-text-tertiary);
}

.migrate__message {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--arkade-text-secondary);
}

.migrate__error {
  margin: 10px 0 0;
  font-size: 12px;
  color: #d03050;
}

.migrate__footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
