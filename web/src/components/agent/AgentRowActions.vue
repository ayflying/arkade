<script setup lang="ts">
/**
 * 服务器行操作区（查看容器 / 编辑 / 重置密钥 / 禁用·启用 / 删除）
 *
 * ## 按钮可用性的判断依据
 *
 * 分两类，**不要混用**：
 *
 * 1. **只改 Hub 侧数据的**（编辑、重置密钥、禁用/启用、删除）
 *    不需要 Agent 在线，因此一律不因 offline/pending 而禁用。
 *
 * 2. **需要 Agent 真的连着才能出数据的**（查看容器）
 *    只有 online 才有意义：
 *    - offline：数据是 Hub 缓存，还能看，所以允许；
 *    - pending：**从来没有连过，一条数据都不存在**，进去只会看到空列表，
 *      然后误以为「这台机器上没有容器」。因此 pending 时禁用，并用 tooltip 说明原因。
 *
 * ## 重置密钥为何每个状态都要给
 * 密钥只在创建时显示一次，丢了就没有第二次机会，只能靠重置。
 * 少了这个入口，用户唯一的出路是删掉服务器重建。
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
  (e: 'reset-token'): void
  (e: 'toggle-disabled'): void
  (e: 'delete'): void
}>()

const disabled = computed(() => isAgentDisabled(props.agent))

/** 是否还没接入过：pending 状态下没有容器数据可看 */
const isPending = computed(() => props.agent.status === 'pending')

/** 禁用/启用按钮的文案：按当前状态显示「下一步能做什么」 */
const toggleLabel = computed(() => (disabled.value ? '启用' : '禁用'))

/** 禁用/启用按钮的 tooltip 说明：解释这个操作的后果 */
const toggleHint = computed(() =>
  disabled.value
    ? '启用后恢复正常管理'
    : '禁用后该服务器的操作入口不可用，不会删除任何容器'
)

/** 「容器」按钮的说明文案 */
const enterHint = computed(() =>
  isPending.value
    ? '这台服务器还没有接入过，暂时没有容器信息。请先在服务器上启动 Agent'
    : '查看该服务器上的容器'
)
</script>

<template>
  <div class="row-actions" @click.stop>
    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <!-- pending 时禁用：进去必然是空列表，会让人误以为机器上真的没有容器 -->
        <n-button size="tiny" tertiary :disabled="busy || isPending" @click="emit('enter')">
          容器
        </n-button>
      </template>
      {{ enterHint }}
    </n-tooltip>

    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary :disabled="busy" @click="emit('edit')">编辑</n-button>
      </template>
      修改显示名、备注与标签
    </n-tooltip>

    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary :disabled="busy" @click="emit('reset-token')">
          重置密钥
        </n-button>
      </template>
      重新生成专属密钥（旧密钥立即失效）
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
