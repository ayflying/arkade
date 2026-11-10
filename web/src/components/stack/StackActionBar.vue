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
  /**
   * 当前账号是否有变更权限（manage）。
   *
   * 这些动作都要求 manage：up/rebuild/remove 后端都会校验。
   * 只读账号看到一排亮着的按钮、点了才被 403，体验很差；
   * 这里直接禁用。**这只是界面收敛，不是安全边界**——
   * 真正的拦截永远在后端。
   */
  permitted?: boolean
  /**
   * 父组件是否能处理「更新镜像」。
   *
   * 该动作需要 agentId + 编排名才能打开实时输出弹窗，本组件两者都没有。
   * 父组件不接 `pull` 事件时这个按钮不显示——宁可没有入口，
   * 也不要留一个点下去什么都不发生（或退回旧 runStackAction）的按钮。
   */
  canPull?: boolean
}

const props = withDefaults(defineProps<Props>(), { deleting: false, permitted: true, canPull: false })

const emit = defineEmits<{
  (e: 'action', action: StackAction): void
  (e: 'delete'): void
  /** 请求父组件打开「更新镜像」输出弹窗 */
  (e: 'pull'): void
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
 * - `offline`：Agent 离线，查不到真实状态，不是「已停止」；
 * - `unknown`：后端也没给出有效状态；
 * - `busy`：Agent 正在对该项目执行操作，此刻读到的是**半写状态**，
 *   既不能判定为运行中也不能判定为已停止。
 *
 * 这三种情况下一律不推断运行/停止，操作按钮统一按不可用处理
 * （上层也用 online 禁用，这里双重保险）。
 *
 * busy 单独列出来是关键：若把它当「不是 offline」放过去，
 * 就会被当成可操作状态，用户可能在 Agent 正在 down 的中途点 up，
 * 两个操作互相踩，结果不可预测。
 */
const stateKnown = computed(
  () => props.state !== 'offline' && props.state !== 'unknown' && props.state !== 'busy'
)

/** 项目是否正在被 Agent 操作中（用于给出「请稍候」的解释） */
const isBusy = computed(() => props.state === 'busy')

/** 综合可用性：有权限 + 在线 + 状态可知 + 无操作在途 */
const canOperate = computed(
  () => props.permitted && props.online && stateKnown.value && !busy.value
)

/**
 * 更新镜像与重建的确认文案。
 *
 * 这两个操作都会重建容器（短暂中断服务），
 * 但用户常常以为是「热更新」，所以必须说清会中断。
 */
async function confirmAndRun(action: StackAction, title: string, content: string): Promise<void> {
  // busy / 离线 / 状态未知时连确认框都不弹：弹了也执行不了，只是多一步
  if (busy.value || !stateKnown.value) return
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
  // stateKnown 已排除 busy：项目正在被操作时不能叠加新动作
  if (busy.value || !props.online || !props.permitted || !stateKnown.value) return
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

/**
 * 更新镜像：不再从这里触发。
 *
 * 「更新镜像」现在是一个带实时命令输出的独立弹窗（StackPullModal），
 * 它需要 agentId + 编排名，而本组件两者都没有（只接收状态与回调）。
 * 所以这里改为把意图抛给父组件，由父组件决定挂载弹窗；
 * 没有父级处理时不显示该按钮，避免点出一个没有内容的旧路径。
 */
const pullAvailable = computed(() => props.canPull === true)

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
      操作中的提示条：按钮全部变灰时，用户需要知道「为什么」。
      没有这句解释，用户会以为界面卡住了或者权限不够。
    -->
    <span v-if="isBusy" class="stack-actions__busy">
      该项目正在执行操作，请等待完成后再继续。
    </span>

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
      v-if="pullAvailable"
      size="small"
      :disabled="!canOperate"
      @click="emit('pull')"
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
      注意这里**保留** online 判断：删除必须由 Agent 在那台机器上真正移除容器和文件，
      Agent 离线时后端会 503 并保留记录，所以离线时禁用而不是让用户点完拿一个报错。
      同时也要 permitted——删除要 manage 权限。
      另外还要排除 stateKnown 里的 busy：
      项目正在被操作时删除，会和进行中的操作互相踩，可能留下半清理状态。
    -->
    <n-button
      size="small"
      type="error"
      secondary
      :disabled="busy || !online || !permitted || !stateKnown"
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
  align-items: center;
  gap: 8px;
}

/* 操作中的解释文案：不抢按钮的视觉重点，但要能读到 */
.stack-actions__busy {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}
</style>
