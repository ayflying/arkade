<script setup lang="ts">
/**
 * 「更新镜像」执行输出弹窗
 *
 * 展示真实命令行输出，不编造进度条——`compose pull` 没有可靠的百分比，
 * 画一个假的反而让人以为卡住了。
 *
 * ## 无障碍
 *
 * 输出区 `role="log"` + `aria-live="polite"`：屏幕阅读器会在内容更新时
 * 朗读新增部分，但不会打断用户正在做的事（`polite` 而非 `assertive`，
 * 日志刷得快时 `assertive` 会让人没法用页面）。
 *
 * ## 自动滚动
 *
 * 只在用户本来就在底部时才跟随新输出。用户往上翻看历史时突然被拽回底部
 * 是最恼人的交互之一。
 */
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { NAlert, NButton, NModal, NSpin, NTag } from 'naive-ui'
import { useStackPullTask } from '@/composables/useStackPullTask'
import { getStackPullTask, startStackPull } from '@/api/stackPullTasks'

interface Props {
  show: boolean
  agentId: string
  stackName: string
  /** 服务器在线状态；离线时不建议开始（但要能查看已有任务） */
  online: boolean
}

const props = defineProps<Props>()
const emit = defineEmits<{ (e: 'update:show', value: boolean): void; (e: 'finished'): void }>()

const runner = useStackPullTask({
  start: (requestId) => startStackPull(props.agentId, props.stackName, requestId),
  fetch: (taskId, after) => getStackPullTask(props.agentId, props.stackName, taskId, after),
  // 校验响应确实属于这个编排，防止串项目时显示别人的任务
  expectedName: props.stackName
})

const {
  task,
  output,
  dropped,
  droppedReasons,
  phase,
  starting,
  uncertain,
  permanentError,
  errorText,
  plainLog
} = runner

const logRef = ref<HTMLElement | null>(null)
/** 用户是否停在底部；默认认为是，这样第一批输出就会跟随 */
const atBottom = ref(true)

/**
 * 最后一个事件的 seq。
 *
 * 自动滚动不能 watch `output.length`：达到条数上限后长度恒定不变，
 * 新内容一直在进来但长度不动，滚动就彻底失效了。
 * seq 是单调递增的，即使裁剪后仍在变，是可靠的触发信号。
 */
const lastSeq = computed(() => {
  const list = output.value
  return list.length === 0 ? 0 : list[list.length - 1].seq
})

/** 终态时再点「开始」要确认，避免把已成功的任务当重跑 */
const showNewTaskConfirm = ref(false)

/** 阶段文案：只说事实，不猜进度 */
const phaseText = computed(() => {
  switch (phase.value) {
    case 'starting':
      return '正在提交任务…'
    case 'queued':
      // 排队还没有真正执行，不能说「正在拉取」，否则用户会误以为已经在跑了
      return '已排队，等待开始'
    case 'running':
      return '正在拉取镜像…'
    case 'done':
      return '已完成'
    case 'failed':
      return '执行失败'
    case 'interrupted':
      return '连接中断'
    case 'blocked':
      // 状态未知：可能在服务器上已经跑起来了，不能说「尚未开始」
      return '状态未知'
    default:
      return '尚未开始'
  }
})

const phaseType = computed<'default' | 'info' | 'success' | 'error' | 'warning'>(() => {
  switch (phase.value) {
    case 'done':
      return 'success'
    case 'failed':
      return 'error'
    case 'interrupted':
    case 'blocked':
      return 'warning'
    case 'running':
    case 'starting':
      return 'info'
    default:
      return 'default'
  }
})

/** 退出码只在真正有值时才显示；0 也要显示出来（成功是有意义的证据） */
const exitText = computed(() => {
  const code = task.value?.exit_code
  if (code === null || code === undefined) return ''
  return `退出码 ${String(code)}`
})

/** 输出为空时给一句解释，避免空白让人以为坏了 */
const emptyHint = computed(() => {
  if (task.value === null) return '点击「开始更新镜像」后，这里会显示实际执行的命令与输出。'
  if (output.value.length === 0) return '任务已创建，等待输出…'
  return ''
})

/** 滚动到底部 */
async function scrollToBottom(): Promise<void> {
  await nextTick()
  const el = logRef.value
  if (el === null) return
  el.scrollTop = el.scrollHeight
}

/** 记录用户是否在底部（留 24px 容差，避免因取整判定失败） */
function handleScroll(): void {
  const el = logRef.value
  if (el === null) return
  atBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight <= 24
}

/**
 * 新输出到达：只有用户本来在底部才跟随。
 *
 * 用 lastSeq 而非 length：条数达到上限后 length 不再变化，
 * watch length 会在长任务里彻底停止跟随。
 */
watch(lastSeq, () => {
  if (atBottom.value) void scrollToBottom()
})

onMounted(() => {
  if (props.show) void scrollToBottom()
})

/** 打开弹窗：已有任务就继续看，没有则等用户点开始 */
watch(
  () => props.show,
  (visible) => {
    if (visible) {
      atBottom.value = true
      void scrollToBottom()
      // 有未结束的任务就接着读（GET 只读，安全）
      if (task.value !== null && !runner.finished.value) void runner.resume()
    }
  }
)

/** 任务结束时通知父组件刷新编排状态（可能显示新镜像） */
watch(
  () => phase.value,
  (next) => {
    if (next === 'done') emit('finished')
  }
)

/**
 * 开始更新。
 *
 * 这里就是唯一的操作确认：按钮明确写着「开始更新镜像」，
 * 上方固定说明只拉不重建。不做额外的确认弹窗。
 */
async function handleStart(): Promise<void> {
  // 已有未结束任务：直接接着看，不重复启动
  if (task.value !== null && !runner.finished.value) {
    await runner.resume()
    return
  }
  // 终态后重跑：必须显式确认，避免把已成功的结果当成"再点一次"
  if (task.value !== null && runner.finished.value) {
    showNewTaskConfirm.value = true
    return
  }
  await runner.start()
}

/** 确认开始一个全新任务（清掉旧结果） */
async function confirmNewTask(): Promise<void> {
  showNewTaskConfirm.value = false
  /*
   * reset 可能被拒绝：如果上一个任务状态不明确（启动请求没结果、
   * 或读取一直失败），清空就会生成新 request_id，等于放弃幂等——
   * 后端可能已有任务在跑。这种情况下改为重试确认，不起新任务。
   */
  if (!runner.reset()) {
    await runner.resume()
    return
  }
  await runner.start()
}

/** 网络不确定时重试确认：复用同一个 request_id，不会重复启动 */
async function handleRetryConfirm(): Promise<void> {
  await runner.start()
}

async function handleResume(): Promise<void> {
  await runner.resume()
}

/**
 * 关闭。
 *
 * **不取消任务**：compose pull 已经在服务器上跑起来了，关窗只是不看。
 * 这一点必须在界面上说清，否则用户会以为关掉就停了。
 */
function close(): void {
  // pause 而非 dispose：关窗只是不看，之后还要能重开接着读
  runner.pause()
  emit('update:show', false)
}

/**
 * 是否显示「开始更新镜像」。
 *
 * 必须排除 uncertain：那种情况下任务可能已在服务器上建好，
 * 再显示「开始」会诱导用户以为还没开始。此时应该显示「重试确认任务」
 * （同一个 request_id，行为一样安全，但文案必须诚实）。
 */
const canStart = computed(
  () => props.online && !starting.value && phase.value !== 'starting' && !uncertain.value
)

/** 只看不改：用于 aria-label 与说明 */
const logLabel = computed(() => `「${props.stackName}」更新镜像的命令输出`)
</script>

<template>
  <n-modal
    :show="props.show"
    preset="card"
    :title="`更新镜像 · ${props.stackName}`"
    class="pull-modal"
    :bordered="false"
    :mask-closable="false"
    @update:show="(v: boolean) => { if (!v) close() }"
  >
    <!--
      事先说清「只拉不重建」：用户点「更新镜像」时大多以为顺带生效，
      实际容器还在跑旧镜像。这条不写清楚就是误解的源头。
    -->
    <n-alert type="info" :bordered="false" class="pull-modal__alert">
      只拉取 compose 中声明的镜像，<strong>不会自动重建或重启容器</strong>。
      镜像更新后需要另行执行「重建」才会生效。
    </n-alert>

    <div v-if="!props.online && task === null" class="pull-modal__offline">
      <n-alert type="warning" :bordered="false">
        服务器当前离线，无法启动更新镜像。恢复在线后可以再试。
      </n-alert>
    </div>

    <!--
      实际执行的命令：原样展示，用户要能看到到底跑的是什么。
      工作目录单独一行小字，标明是**Agent 上报的实际 cwd**，
      不是可以随意指定的参数——不要让人以为能在这里换目录执行。
    -->
    <div v-if="task !== null" class="pull-modal__command">
      <span class="pull-modal__command-label">执行命令</span>
      <code class="pull-modal__command-text">{{ task.command }}</code>
      <span v-if="task.working_dir !== ''" class="pull-modal__workdir">
        工作目录 {{ task.working_dir }}
      </span>
    </div>

    <div class="pull-modal__status">
      <n-tag size="small" :bordered="false" :type="phaseType">{{ phaseText }}</n-tag>
      <span v-if="exitText !== ''" class="pull-modal__exit">{{ exitText }}</span>
      <!-- 只在真正执行时转圈：queued 阶段转圈会让人以为已经在跑了 -->
      <n-spin v-if="starting || phase === 'running'" size="small" />
    </div>

    <!--
      错误分档显示，三档互斥：
      1. uncertain / interrupted：读取或启动结果不确定，给重试入口；
      2. 启动被明确拒绝（403/503/其它已知错误）：任务根本没建出来，
         必须把原因说出来，不能因为 phase 还停在 idle 就吞掉；
      3. 任务自身 error：真正的执行失败。
      第 2 档之前被漏掉——条件硬绑在 phase==='failed' 上，而启动失败时
      task 为 null、phase 仍是 idle，于是错误文案永远不显示，用户点了没反应。
    -->
    <n-alert v-if="uncertain" type="warning" :bordered="false" class="pull-modal__alert">
      {{ errorText }}
    </n-alert>
    <n-alert
      v-else-if="phase === 'interrupted'"
      type="warning"
      :bordered="false"
      class="pull-modal__alert"
    >
      {{ errorText }}
      <n-button
        text
        size="tiny"
        class="pull-modal__retry"
        @click="handleResume"
      >
        {{ permanentError ? '重新读取' : '立即重试' }}
      </n-button>
    </n-alert>
    <!-- 启动阶段的明确错误：没有任务，也没有 uncertain 标记 -->
    <n-alert
      v-else-if="errorText !== '' && (task === null || task.error === '')"
      type="error"
      :bordered="false"
      class="pull-modal__alert"
    >
      {{ errorText }}
    </n-alert>

    <!-- 任务自身的失败原因 -->
    <n-alert v-if="task !== null && task.error !== ''" type="error" :bordered="false" class="pull-modal__alert">
      {{ task.error }}
    </n-alert>

    <!-- 输出被省略的三种原因分开说，不能只给一句笼统提示 -->
    <div v-if="dropped" class="pull-modal__truncated">
      <span v-if="droppedReasons.includes('trimmed')">输出过长，较早的内容已省略（仅保留最近 256 KB / 2000 条）。</span>
      <span v-if="droppedReasons.includes('server')">服务器端的输出已被截断，完整日志请到服务器查看。</span>
      <span v-if="droppedReasons.includes('gap')">日志中间有缺失（部分输出未能接收）。</span>
    </div>

    <!--
      role="log" + aria-live="polite"：内容更新时朗读新增部分。
      文本一律走插值（不经 v-html），且已剥 ANSI 转义，
      日志里出现的任何外部内容都会被转义成纯文本。
    -->
    <pre
      ref="logRef"
      class="pull-modal__log"
      role="log"
      aria-live="polite"
      :aria-label="logLabel"
      @scroll="handleScroll"
    >{{ plainLog }}<span v-if="emptyHint !== ''" class="pull-modal__empty">{{ emptyHint }}</span></pre>

    <div class="pull-modal__footer">
      <!-- 首次开始 / 终态后重跑：同一个按钮，文案区分语义 -->
      <n-button
        v-if="canStart && (task === null || runner.finished.value)"
        type="primary"
        size="small"
        :loading="starting"
        @click="handleStart"
      >
        {{ task === null ? '开始更新镜像' : '重新拉取' }}
      </n-button>
      <n-button
        v-else-if="uncertain"
        type="primary"
        size="small"
        :loading="starting"
        @click="handleRetryConfirm"
      >
        重试确认任务
      </n-button>
      <span v-else-if="task !== null && !runner.finished.value" class="pull-modal__hint">
        关闭这个窗口不会停止任务，任务会继续在服务器上执行。
      </span>
      <n-button size="small" @click="close">
        {{ runner.finished.value || task === null ? '关闭' : '在后台继续' }}
      </n-button>
    </div>

    <!-- 终态后重跑需要明确确认 -->
    <n-modal
      v-model:show="showNewTaskConfirm"
      preset="dialog"
      type="warning"
      title="确认重新拉取镜像？"
      positive-text="重新拉取"
      negative-text="取消"
      @positive-click="confirmNewTask"
    >
      上一次拉取已经结束。重新拉取会创建一个新任务，并清空当前窗口里的日志。
    </n-modal>
  </n-modal>
</template>

<style scoped>
.pull-modal__alert {
  margin-bottom: 10px;
}

.pull-modal__offline {
  margin-bottom: 10px;
}

.pull-modal__command {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 10px;
}

.pull-modal__command-label {
  color: var(--arkade-text-tertiary);
  font-size: 12px;
}

.pull-modal__command-text {
  display: block;
  padding: 6px 8px;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  color: var(--arkade-text-secondary);
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
  border-radius: 4px;
  word-break: break-all;
}

.pull-modal__status {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.pull-modal__exit {
  color: var(--arkade-text-tertiary);
  font-size: 12px;
}

.pull-modal__truncated {
  color: var(--arkade-text-tertiary);
  font-size: 12px;
  margin-bottom: 6px;
}

/* 固定高度 + 内滚动：长日志不会把弹窗撑爆 */
.pull-modal__log {
  height: 320px;
  margin: 0 0 12px;
  padding: 8px 10px;
  overflow: auto;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.55;
  color: var(--arkade-text-primary);
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
  border-radius: 4px;
}

.pull-modal__empty {
  color: var(--arkade-text-tertiary);
}

.pull-modal__footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}

.pull-modal__hint {
  color: var(--arkade-text-tertiary);
  font-size: 12px;
}

.pull-modal__retry {
  margin-left: 6px;
}
</style>
