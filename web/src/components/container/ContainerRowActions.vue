<script setup lang="ts">
/**
 * 容器行操作按钮组（启动 / 停止 / 重启 / 日志 / 删除）
 *
 * 规则来源：utils/agentAction
 * - 服务器离线：所有需要 Agent 转发的操作都不可用（请求必挂），
 *   按钮 disabled 并给出 tooltip 原因；
 * - 状态不匹配：运行中不显示可用的「启动」，已停止不显示可用的「停止」，
 *   用 disabled + tooltip 表达，而不是隐藏按钮（隐藏会让用户以为功能缺失）。
 *
 * 各操作的可用条件：
 * - 启动：非运行中且非过渡态；
 * - 停止：运行中；
 * - 重启：**只要状态可知就可用**（重启本身就是用来修「部分运行/卡住」的，
 *   若只在 running 时可用，恰恰在最需要它的时候点不了）；
 * - 日志：在线即可（读取操作，也需 Agent 转发）；
 * - 删除：在线即可（运行中的容器由后端 force 强制停止后删除）。
 *
 * 事件只向上抛，不直接调接口：页面负责二次确认、接口调用与提示，
 * 保证「确认 → 请求 → 反馈」的流程只在一个地方编排。
 */
import { computed } from 'vue'
import { NButton, NTooltip } from 'naive-ui'
import type { Agent } from '@/types/agent'
import type { Container } from '@/types/container'
import {
  canRestartContainer,
  canStartContainer,
  canStopContainer,
  containerActionBlockReason,
  isAgentOnline
} from '@/utils/agentAction'

interface Props {
  agent: Agent | null | undefined
  container: Container
  /** 该行有操作在进行（启停/重启/删除） */
  busy?: boolean
}

const props = withDefaults(defineProps<Props>(), { busy: false })

const emit = defineEmits<{
  (e: 'start'): void
  (e: 'stop'): void
  (e: 'restart'): void
  (e: 'logs'): void
  (e: 'delete'): void
}>()

/** 容器级阻断原因（离线 / 中间态），来自统一判断 */
const blockReason = computed(() => containerActionBlockReason(props.agent, props.container))

/** 启动是否可用：未阻断 + 状态允许启动 + 不在请求中 */
const canStart = computed(
  () => blockReason.value === null && canStartContainer(props.container.state) && !props.busy
)

/** 停止是否可用：未阻断 + 状态允许停止 + 不在请求中 */
const canStop = computed(
  () => blockReason.value === null && canStopContainer(props.container.state) && !props.busy
)

/** 重启是否可用：未阻断 + 状态允许重启 + 不在请求中 */
const canRestart = computed(
  () => blockReason.value === null && canRestartContainer(props.container.state) && !props.busy
)

/** 日志与删除：只要 Agent 在线即可（离线时请求必然失败，直接禁用） */
const online = computed(() => isAgentOnline(props.agent))
const canViewLogs = computed(() => online.value && !props.busy)
const canDelete = computed(() => online.value && !props.busy)

/**
 * 各按钮的禁用原因：
 * - 优先级：离线/状态阻断 > 状态不匹配 > 请求中；
 * - 请求中不展示 tooltip 干扰（loading 本身就是反馈）。
 */
const startReason = computed<string>(() => {
  if (blockReason.value !== null) return blockReason.value
  if (props.busy) return ''
  if (!canStartContainer(props.container.state)) return '容器已在运行，无需启动'
  return ''
})

const stopReason = computed<string>(() => {
  if (blockReason.value !== null) return blockReason.value
  if (props.busy) return ''
  if (!canStopContainer(props.container.state)) return '容器未在运行，无需停止'
  return ''
})

const restartReason = computed<string>(() => {
  if (blockReason.value !== null) return blockReason.value
  if (props.busy) return ''
  if (!canRestartContainer(props.container.state)) {
    return `容器当前状态为 ${props.container.state}，暂不支持重启`
  }
  return ''
})

/** 日志与删除共用「是否在线」的说明，但措辞各有侧重 */
const logsReason = computed<string>(() => (online.value ? '' : '服务器已离线，无法拉取日志'))
const deleteReason = computed<string>(() => (online.value ? '' : '服务器已离线，无法删除容器'))
</script>

<template>
  <div class="container-actions">
    <!-- 启动 -->
    <n-tooltip :disabled="startReason === ''" trigger="hover" placement="top">
      <template #trigger>
        <span class="container-actions__slot">
          <n-button size="tiny" tertiary type="primary" :disabled="!canStart" @click="emit('start')">
            启动
          </n-button>
        </span>
      </template>
      {{ startReason }}
    </n-tooltip>

    <!-- 停止 -->
    <n-tooltip :disabled="stopReason === ''" trigger="hover" placement="top">
      <template #trigger>
        <span class="container-actions__slot">
          <n-button size="tiny" tertiary type="warning" :disabled="!canStop" @click="emit('stop')">
            停止
          </n-button>
        </span>
      </template>
      {{ stopReason }}
    </n-tooltip>

    <!-- 重启 -->
    <n-tooltip :disabled="restartReason === ''" trigger="hover" placement="top">
      <template #trigger>
        <span class="container-actions__slot">
          <n-button size="tiny" tertiary :disabled="!canRestart" @click="emit('restart')">
            重启
          </n-button>
        </span>
      </template>
      {{ restartReason }}
    </n-tooltip>

    <!-- 日志 -->
    <n-tooltip :disabled="logsReason === ''" trigger="hover" placement="top">
      <template #trigger>
        <span class="container-actions__slot">
          <n-button size="tiny" tertiary :disabled="!canViewLogs" @click="emit('logs')">
            日志
          </n-button>
        </span>
      </template>
      {{ logsReason }}
    </n-tooltip>

    <!-- 删除：危险操作，红色 + 页面里的二次确认（含卷勾选项） -->
    <n-tooltip :disabled="deleteReason === ''" trigger="hover" placement="top">
      <template #trigger>
        <span class="container-actions__slot">
          <n-button size="tiny" tertiary type="error" :disabled="!canDelete" @click="emit('delete')">
            删除
          </n-button>
        </span>
      </template>
      {{ deleteReason }}
    </n-tooltip>

    <!-- 操作进行中的轻提示，避免用户以为点击没反应 -->
    <span v-if="busy" class="container-actions__busy">处理中…</span>
  </div>
</template>

<style scoped>
.container-actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  /* 5 个按钮在窄屏下允许换行，避免把单元格撑得过宽 */
  flex-wrap: wrap;
}

/* disabled 按钮不派发鼠标事件，tooltip 需要挂在可交互的外层容器上 */
.container-actions__slot {
  display: inline-flex;
}

.container-actions__busy {
  font-size: 12px;
  color: #8b9099;
}
</style>
