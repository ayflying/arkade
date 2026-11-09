<script setup lang="ts">
/**
 * 编排操作栏：启动 / 停止 / 重启 / 更新镜像 / 重建 / 备份 / 删除
 *
 * 单独成组件的原因：7 个操作的消息文案、确认策略、忙碌态管理放一起已经有几十行，
 * 再塞进详情页会让详情页难以阅读。
 *
 * 操作分级（决定是否需要二次确认）：
 * - **安全/可逆**：启动、停止、重启、备份 —— 不弹确认，直接执行（误点代价低，弹窗反而烦）；
 * - **有影响**：更新镜像（会拉新镜像并重建）、重建（会短暂中断服务）—— 二次确认；
 * - **不可逆**：删除 —— 由外层用带勾选项的确认框处理。
 *
 * 忙碌态：任一操作在途时禁用所有按钮。因为 compose 的多个操作同时执行
 * 会在后端争抢同一份项目，产生难以理解的错误，串行更稳妥。
 */
import { computed, ref } from 'vue'
import { NButton } from 'naive-ui'
import type { StackAction, StackState } from '@/types/stack'
import { confirmAction } from '@/utils/confirm'

interface Props {
  /** 当前编排状态，用于决定哪些操作可用 */
  state: StackState
  /** 服务器是否在线 */
  online: boolean
  /** 删除中（外部触发的忙碌态） */
  deleting?: boolean
}

const props = withDefaults(defineProps<Props>(), { deleting: false })

const emit = defineEmits<{
  (e: 'action', action: StackAction): void
  (e: 'delete'): void
}>()

/** 正在执行的操作名；空字符串表示空闲 */
const runningAction = ref<StackAction | ''>('')

/** 是否有操作在途：在途时禁用全部按钮，避免并发操作互相干扰 */
const busy = computed(() => runningAction.value !== '' || props.deleting)

/** 是否已停止：停止状态下才允许「启动」，运行中才允许「停止」 */
const isStopped = computed(() => props.state === 'stopped')

/**
 * 状态是否「可知」。
 *
 * Agent 离线时后端把编排状态标成 'offline'——这表示查不到真实状态，
 * 而不是「已停止」。此时既不该判定为可启动，也不该判定为可停止，
 * 所有操作按钮统一按不可用处理（上层也用 online 禁用，这里双重保险）。
 */
const stateKnown = computed(() => props.state !== 'offline' && props.state !== 'unknown')

/** 综合可用性：在线 + 状态可知 + 无操作在途 */
const canOperate = computed(() => props.online && stateKnown.value && !busy.value)

/**
 * 更新镜像与重建的确认文案。
 *
 * 这两个操作都会重建容器（短暂中断服务），
 * 但用户常常以为是「热更新」，所以必须说清会中断。
 */
async function confirmAndRun(action: StackAction, title: string, content: string): Promise<void> {
  if (busy.value) return
  const ok = await confirmAction({
    title,
    content,
    positiveText: '继续',
    negativeText: '取消'
  })
  if (!ok) return
  run(action)
}

/** 直接执行（无需确认的操作） */
function run(action: StackAction): void {
  if (busy.value || !props.online) return
  runningAction.value = action
  emit('action', action)
}

/** 停止：运行中才可用 */
function handleStop(): void {
  if (isStopped.value) return
  run('stop')
}

/** 启动：已停止才可用 */
function handleStart(): void {
  if (!isStopped.value) return
  run('start')
}

/** 重启：任何时候都可用（包括部分运行状态，重启常用来修复部分运行） */
function handleRestart(): void {
  run('restart')
}

/** 更新镜像：拉取最新镜像并重建容器 */
function handlePull(): void {
  void confirmAndRun(
    'pull',
    '确认更新镜像？',
    '将拉取 compose 文件中声明的最新镜像，并用新镜像重建容器。\n\n' +
      '重建期间服务会短暂中断（通常几秒到几十秒）。'
  )
}

/** 重建：强制重新创建所有容器 */
function handleRecreate(): void {
  void confirmAndRun(
    'recreate',
    '确认重建编排？',
    '将销毁并重新创建该编排的所有容器。\n\n' +
      '容器内未持久化的数据会丢失，服务会短暂中断。数据卷不受影响。'
  )
}

/** 备份：导出配置与数据卷，属于安全操作 */
function handleBackup(): void {
  run('backup')
}

/**
 * 外部操作完成后由父组件调用，清掉忙碌态。
 * 若不清理，按钮会一直是 loading 状态。
 */
function finish(): void {
  runningAction.value = ''
}

defineExpose({ finish })
</script>

<template>
  <div class="stack-actions">
    <!--
      启动用 primary 主色，但只在真正可点时才上主色。
      服务运行中时「启动」是禁用的，若仍画成醒目的蓝按钮，
      视觉上会暗示「可以点这里」，与实际矛盾——实测截图里就看到这个误导。
    -->
    <n-button
      size="small"
      :type="isStopped && canOperate ? 'primary' : 'default'"
      :disabled="!canOperate || !isStopped"
      :loading="runningAction === 'start'"
      @click="handleStart"
    >
      启动
    </n-button>

    <n-button
      size="small"
      :disabled="!canOperate || isStopped"
      :loading="runningAction === 'stop'"
      @click="handleStop"
    >
      停止
    </n-button>

    <n-button
      size="small"
      :disabled="!canOperate"
      :loading="runningAction === 'restart'"
      @click="handleRestart"
    >
      重启
    </n-button>

    <n-button
      size="small"
      :disabled="!canOperate"
      :loading="runningAction === 'pull'"
      @click="handlePull"
    >
      更新镜像
    </n-button>

    <n-button
      size="small"
      :disabled="!canOperate"
      :loading="runningAction === 'recreate'"
      @click="handleRecreate"
    >
      重建
    </n-button>

    <n-button
      size="small"
      :disabled="!canOperate"
      :loading="runningAction === 'backup'"
      @click="handleBackup"
    >
      备份
    </n-button>

    <!--
      删除是最危险的操作：二次确认 + 红色。
      这里不传 online 判断，是因为删除的是 Hub 里的记录 + 容器，
      即使 Agent 离线也应当允许用户清理纳管关系（是否真的清掉容器由后端处理）。
    -->
    <n-button
      size="small"
      type="error"
      secondary
      :disabled="busy"
      :loading="deleting"
      @click="emit('delete')"
    >
      删除
    </n-button>
  </div>
</template>

<style scoped>
.stack-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}
</style>
