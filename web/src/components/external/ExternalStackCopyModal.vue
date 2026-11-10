<script setup lang="ts">
/**
 * 复制配置到其他服务器
 *
 * ## 名字刻意不叫「迁移」
 *
 * 这一操作**只复制 compose 配置本身**：不含 `.env`、不含 build 上下文、
 * 不含数据卷、不含镜像，目标上也不会启动任何容器，源配置完全保留。
 * 叫「迁移」会让用户以为容器和数据都搬过去了——那是另一件更危险的事。
 *
 * 所以弹窗里必须把「不会包含什么」摆在明面上，而不是只写「复制成功」。
 */
import { computed, ref, watch } from 'vue'
import { NAlert, NButton, NInput, NModal, NSelect } from 'naive-ui'
import type { Agent } from '@/types/agent'

interface Props {
  show: boolean
  sourceName: string
  /** 当前内容（来自编辑器，可能是草稿） */
  content: string
  /** 当前所在服务器，目标里要排除它 */
  agentId: string
  agents: Agent[]
  loading: boolean
  errorText: string
}

const props = defineProps<Props>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  submit: [payload: { targetAgentId: string; targetName: string }]
}>()

/** 目标服务器 */
const targetAgentId = ref('')
/** 目标服务器上的编排名 */
const targetName = ref('')

watch(
  () => props.show,
  (show) => {
    if (!show) return
    targetAgentId.value = ''
    targetName.value = props.sourceName
  }
)

/** 候选服务器：排除源服务器自己 */
const agentOptions = computed(() =>
  props.agents
    .filter((a) => a.id !== props.agentId)
    .map((a) => ({
      label: a.status === 'online' ? a.display_name || a.id : `${a.display_name || a.id}（离线）`,
      value: a.id,
      // 离线的目标不能选：复制要求目标在线，早挡比点了再报错好
      disabled: a.status !== 'online'
    }))
)

/** 当前选中的目标是否离线（用于提示） */
const targetOffline = computed(() => {
  if (targetAgentId.value === '') return false
  const found = props.agents.find((a) => a.id === targetAgentId.value)
  return found !== undefined && found.status !== 'online'
})

const canSubmit = computed(
  () =>
    !props.loading &&
    targetAgentId.value !== '' &&
    targetName.value.trim() !== '' &&
    !targetOffline.value
)

function handleSubmit(): void {
  if (!canSubmit.value) return
  emit('submit', { targetAgentId: targetAgentId.value, targetName: targetName.value.trim() })
}
</script>

<template>
  <n-modal
    :show="show"
    preset="card"
    title="复制配置到其他服务器"
    :style="{ maxWidth: '520px', width: '92vw' }"
    :bordered="false"
    @update:show="(v: boolean) => emit('update:show', v)"
  >
    <div class="copy">
      <div class="copy__row">
        <span class="copy__label">源项目</span>
        <span class="copy__value copy__value--mono">{{ sourceName }}</span>
      </div>

      <div class="copy__row">
        <span class="copy__label">目标服务器</span>
        <n-select
          v-model:value="targetAgentId"
          :options="agentOptions"
          placeholder="选择目标服务器"
          class="copy__select"
          :disabled="loading"
        />
      </div>

      <div class="copy__row">
        <span class="copy__label">目标编排名</span>
        <n-input
          v-model:value="targetName"
          placeholder="目标服务器上的编排名"
          :disabled="loading"
        />
      </div>

      <!--
        范围说明必须写全：这是本操作最容易误导用户的地方。
        只讲「会复制什么」不够，必须同时讲「不会复制什么」。
      -->
      <n-alert type="warning" :bordered="false" class="copy__notice" title="这不是完整迁移">
        <p class="copy__notice-line">本次<strong>只复制 compose 配置文本</strong>，并把配置登记到目标服务器。</p>
        <ul class="copy__list">
          <li><strong>不复制</strong> <code>.env</code> 文件与其中的环境变量</li>
          <li><strong>不复制</strong> build 上下文与本地构建镜像</li>
          <li><strong>不复制</strong> 数据卷中的任何数据</li>
          <li><strong>不复制</strong> 镜像，需要自行在目标服务器拉取</li>
          <li><strong>不启动</strong>目标上的任何容器</li>
        </ul>
        <p class="copy__notice-line">源服务器上的配置与容器都不受影响。</p>
        <p class="copy__notice-line">
          目标服务器上会得到一份<strong>尚未同步</strong>的配置记录，之后也不会自动启动。
        </p>
      </n-alert>

      <!-- 目标必须在线：离线时复制无法完成，先说清楚而不是等报错 -->
      <n-alert v-if="targetOffline" type="error" :bordered="false">
        目标服务器当前离线，无法复制配置。请等它上线后再试。
      </n-alert>

      <p v-if="errorText !== ''" class="copy__error">{{ errorText }}</p>
    </div>

    <template #footer>
      <div class="copy__footer">
        <n-button size="small" :disabled="loading" @click="emit('update:show', false)">取消</n-button>
        <n-button size="small" type="primary" :loading="loading" :disabled="!canSubmit" @click="handleSubmit">
          确认复制
        </n-button>
      </div>
    </template>
  </n-modal>
</template>

<style scoped>
.copy {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.copy__row {
  display: flex;
  align-items: center;
  gap: 10px;
}

.copy__label {
  width: 88px;
  flex: none;
  font-size: 13px;
  color: var(--arkade-text-secondary);
}

.copy__value {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.copy__value--mono {
  font-family: var(--arkade-mono-font);
}

.copy__select {
  flex: 1;
  min-width: 0;
}

.copy__notice-line {
  margin: 0;
}

.copy__list {
  margin: 6px 0;
  padding-left: 18px;
}

.copy__error {
  margin: 0;
  font-size: 13px;
  color: var(--arkade-error-color, #d03050);
}

.copy__footer {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
}
</style>
