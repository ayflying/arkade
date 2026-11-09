<script setup lang="ts">
/**
 * 服务器行操作区（查看容器 / 编辑 / 禁用·启用 / 删除）
 *
 * ## 与「离线」有关的按钮可用性
 *
 * 「查看容器」「编辑」「启用」**不加在线限制**：
 * - 查看容器：数据是 Hub 缓存的，离线也能看；
 * - 编辑：改的是 Hub 里的显示名/备注/标签，根本不经过 Agent；
 * - 启用：改的是 Hub 侧的开关状态，同样不需要 Agent 在线。
 *
 * 只有「禁用」需要限制吗？也不需要——它同样只改 Hub 状态。
 * 因此本组按钮全部只看「是否被禁用」和「是否正在请求中」，
 * 这与容器启停按钮（必须 Agent 在线）的规则不同，不要混用判断。
 */
import { computed } from 'vue'
import { NButton, NTooltip } from 'naive-ui'
import type { Agent } from '@/types/agent'
import { isAgentDisabled } from '@/utils/agentDisplay'

interface Props {
  agent: Agent
  /** 该行是否有操作在途（删除/禁用等），在途时禁用全部按钮防连点 */
  busy?: boolean
}

const props = withDefaults(defineProps<Props>(), { busy: false })

const emit = defineEmits<{
  (e: 'enter'): void
  (e: 'edit'): void
  (e: 'toggle-disabled'): void
  (e: 'delete'): void
}>()

const disabled = computed(() => isAgentDisabled(props.agent))

/** 禁用/启用按钮的文案：按当前状态显示「下一步能做什么」 */
const toggleLabel = computed(() => (disabled.value ? '启用' : '禁用'))

/** 禁用/启用按钮的 tooltip 说明：解释这个操作的后果 */
const toggleHint = computed(() =>
  disabled.value
    ? '启用后恢复正常管理'
    : '禁用后该服务器的操作入口不可用，不会删除任何容器'
)
</script>

<template>
  <div class="row-actions" @click.stop>
    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary :disabled="busy" @click="emit('enter')">容器</n-button>
      </template>
      查看该服务器上的容器
    </n-tooltip>

    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary :disabled="busy" @click="emit('edit')">编辑</n-button>
      </template>
      修改显示名、备注与标签
    </n-tooltip>

    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary :disabled="busy" @click="emit('toggle-disabled')">
          {{ toggleLabel }}
        </n-button>
      </template>
      {{ toggleHint }}
    </n-tooltip>

    <!-- 删除是危险操作：红色 + 页面里的二次确认（确认文案由页面负责） -->
    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary type="error" :disabled="busy" @click="emit('delete')">
          删除
        </n-button>
      </template>
      仅移除纳管关系，不删除服务器上的容器
    </n-tooltip>
  </div>
</template>

<style scoped>
.row-actions {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  /* 按钮组在窄屏下允许换行，避免撑破单元格 */
  flex-wrap: wrap;
}
</style>
