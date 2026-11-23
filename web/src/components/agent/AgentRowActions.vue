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
  /** 该服务器的更新正在观察中：只锁「更新」按钮，不牵连同一行的其它入口 */
  updating?: boolean
}

const props = withDefaults(defineProps<Props>(), { busy: false, updating: false })

const emit = defineEmits<{
  (e: 'enter'): void
  (e: 'edit'): void
  (e: 'reset-token'): void
  (e: 'toggle-disabled'): void
  (e: 'delete'): void
  (e: 'self-update'): void
  /** 待接入服务器的「接入信息」：换发新密钥并再次展示 */
  (e: 'renew-enrollment'): void
}>()

const disabled = computed(() => isAgentDisabled(props.agent))
// 内置本机没有网络令牌；关闭入口由 Hub local.enabled 配置负责。
const isLocal = computed(() => props.agent.id === 'agent-local')

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

/** 「进入」按钮的说明文案 */
const enterHint = computed(() =>
  isPending.value
    ? '这台服务器还没有接入过，暂时没有编排信息。请先在服务器上启动 Agent'
    : '进入该服务器：默认打开容器编排'
)

/** 是否在线：更新需要 Agent 真的连着才能下发 */
const isOnline = computed(() => props.agent.status === 'online')

/**
 * 「更新」按钮的说明文案。
 *
 * 三种情况分开说清楚，因为它们的**下一步动作完全不同**：
 * - 在线 → 可以更新（但要提醒会短暂中断）；
 * - 离线 → 连命令都发不出去，先恢复在线；
 * - 被禁用 → 操作入口整体关闭，先去启用。
 */
const updateHint = computed(() => {
  if (props.updating) return '这台服务器正在更新中，更新完成后自动解除'
  if (disabled.value) return '该服务器已被禁用，请先启用后再更新'
  if (!isOnline.value) return '服务器当前离线，恢复在线后才能更新'
  return '更新这台服务器上的 Arkade 组件镜像（会短暂中断，数据不受影响）'
})

/**
 * 更新按钮是否可点：只有在线且未被禁用才下发命令。
 *
 * updating 只锁自己这一行；其它服务器的行各自独立，
 * 因此这里不能引用页面级的共享 busy 状态（否则又变成全局锁）。
 */
const updateDisabled = computed(() => props.busy || props.updating || disabled.value || !isOnline.value)
</script>

<template>
  <div class="row-actions" @click.stop>
    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <!--
          待接入时给「接入信息」而不是「容器」：这台机器还没有任何容器可看，
          用户此刻真正需要的是密钥和编排示例（过几天回来也还能再看一遍）。
        -->
        <n-button
          v-if="isPending && !isLocal"
          size="tiny"
          type="primary"
          secondary
          :disabled="busy"
          @click="emit('renew-enrollment')"
        >
          接入信息
        </n-button>
        <!-- pending 时禁用：进去必然是空列表，会让人误以为机器上真的没有编排 -->
        <n-button v-else size="tiny" tertiary :disabled="busy || isPending" @click="emit('enter')">
          进入
        </n-button>
      </template>
      {{ isPending && !isLocal ? '重新生成接入密钥并查看容器编排示例（旧密钥立即失效）' : enterHint }}
    </n-tooltip>

    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary :disabled="busy" @click="emit('edit')">编辑</n-button>
      </template>
      修改显示名、备注与标签
    </n-tooltip>

    <n-tooltip trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary :disabled="updateDisabled" @click="emit('self-update')">
          {{ updating ? '更新中' : '更新' }}
        </n-button>
      </template>
      {{ updateHint }}
    </n-tooltip>

    <n-tooltip v-if="!isLocal" trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary :disabled="busy" @click="emit('reset-token')">
          重置密钥
        </n-button>
      </template>
      重新生成专属密钥（旧密钥立即失效）
    </n-tooltip>

    <n-tooltip v-if="!isLocal" trigger="hover" placement="top">
      <template #trigger>
        <n-button size="tiny" tertiary :disabled="busy" @click="emit('toggle-disabled')">
          {{ toggleLabel }}
        </n-button>
      </template>
      {{ toggleHint }}
    </n-tooltip>

    <!-- 删除是危险操作：红色 + 页面里的二次确认（确认文案由页面负责） -->
    <n-tooltip v-if="!isLocal" trigger="hover" placement="top">
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
