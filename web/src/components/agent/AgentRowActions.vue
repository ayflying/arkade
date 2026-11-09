<script setup lang="ts">
/**
 * 服务器行操作区（查看容器 / 进入）
 *
 * 关键需求：**离线行不能发出永远不返回的请求**。
 * 因此这里不只是把按钮 disabled，还用 tooltip 明确告诉用户「为什么不能点」
 * （来自 utils/agentAction 的统一文案），避免出现「按钮灰着但没人知道原因」。
 */
import { computed } from 'vue'
import { NButton, NTooltip } from 'naive-ui'
import type { Agent } from '@/types/agent'
import { isAgentOnline } from '@/utils/agentAction'
import { mapAgentStatus } from '@/utils/statusMap'

interface Props {
  agent: Agent
  /** 该行是否正在执行操作（本页暂无写操作，保留给后续扩展，避免引入再做破坏性改动） */
  busy?: boolean
}

const props = withDefaults(defineProps<Props>(), { busy: false })

const emit = defineEmits<{
  (e: 'enter'): void
}>()

const online = computed(() => isAgentOnline(props.agent))

/**
 * 禁用原因文案：
 * - 离线：直接说明离线；
 * - 升级中/未知：说明当前状态不允许操作。
 */
const disabledReason = computed<string>(() => {
  if (online.value) return ''
  const { label } = mapAgentStatus(props.agent.status)
  return `服务器当前状态为「${label}」，无法进行操作`
})

function handleEnter(): void {
  if (!online.value) return
  emit('enter')
}
</script>

<template>
  <n-tooltip :disabled="online" trigger="hover" placement="top">
    <template #trigger>
      <!-- 
        包一层 span：disabled 的按钮不触发鼠标事件，tooltip 会失效，
        因此把 tooltip 挂在能接收事件的父元素上。
      -->
      <span class="row-actions" @click.stop>
        <n-button size="small" tertiary :disabled="!online" :loading="busy" @click="handleEnter">
          查看容器
        </n-button>
      </span>
    </template>
    {{ disabledReason }}
  </n-tooltip>
</template>

<style scoped>
.row-actions {
  display: inline-flex;
}
</style>
