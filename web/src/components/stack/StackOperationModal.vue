<script setup lang="ts">
/**
 * 编排操作通用弹窗：真实命令 + 实时输出
 *
 * 所有编辑动作（启动/停止/重启/更新/重建/备份/删除/保存并部署）共用。
 * 展示真实命令行输出，**不编造进度条**——这些动作没有可靠的百分比，
 * 画一个假的反而让人以为卡住了。
 *
 * ## 多命令分段
 *
 * Agent 在每条真实命令执行前发 `command` 事件。据此把日志切成「第 N 步」，
 * 用户能看出是在逐步执行，而不是一堆输出混在一起。
 *
 * ## 无障碍
 *
 * 输出区 `role="log"` + `aria-live="polite"`：内容更新时朗读新增部分，
 * 但不打断用户（`assertive` 在日志刷得快时会让人没法用页面）。
 *
 * ## 自动滚动
 *
 * 只在用户本来就在底部时跟随。用户往上翻历史时突然被拽回底部是最恼人的交互。
 */
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { NAlert, NButton, NCheckbox, NModal, NSpin, NTag } from 'naive-ui'
import { useStackOperationTask } from '@/composables/useStackOperationTask'
import { getStackOperationTask, startStackOperation } from '@/api/stackOperationTasks'
import type { StackOperationAction } from '@/types/stackOperationTask'
import { STACK_OPERATION_META, isConfirmedRemove } from '@/types/stackOperationTask'
import { decideEntry, shouldSnapshot } from '@/utils/stackOperationEntry'

interface Props {
  show: boolean
  agentId: string
  stackName: string
  /** 要执行的动作 */
  action: StackOperationAction
  online: boolean
  /** 删除时是否同时删数据卷（外部传入初值，弹窗内可改） */
  removeVolumes?: boolean
  /** 备份时是否包含数据卷 */
  includeVolumes?: boolean
  /** 保存并部署：compose 内容 */
  content?: string
  /** 保存并部署：.env 内容；undefined = 保留原文件 */
  envContent?: string
  /** 保存并部署：读取时的版本号（乐观锁，必填） */
  version?: number
  /** 保存并部署：可选的内容哈希校验 */
  expectedConfigHash?: string
  /** 新建编排（走同一 POST，带 create:true） */
  create?: boolean
  /** 是否已经确认过（父级做过二次确认），为真时打开即自动 start */
  autoStart?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  removeVolumes: false,
  includeVolumes: true,
  content: undefined,
  envContent: undefined,
  version: undefined,
  expectedConfigHash: undefined,
  create: false,
  autoStart: false
})

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  (e: 'finished', task: unknown): void
  (e: 'removed'): void
  /** 该动作是否有任务正在执行（父级据此禁用冲突动作） */
  (e: 'busy', value: boolean): void
}>()

const meta = computed(() => STACK_OPERATION_META[props.action])

/** 删除时是否同时删卷：弹窗内的勾选（外部初值可被用户改） */
const removeVolumesLocal = ref(props.removeVolumes)
/** 备份是否包含卷 */
const includeVolumesLocal = ref(props.includeVolumes)

/**
 * 打开弹窗那一刻的提交快照。
 *
 * **关键**：执行时绝不能读 live props。
 * 弹窗开着的时候父组件（配置编辑器）仍在编辑，props 会一直变；
 * 若闭包直接读 props，用户确认的内容和真正提交的内容就不是同一份——
 * 他确认的是 A，发出去的却是后来改的 B。更糟的是重开弹窗执行"旧任务"时
 * 会把当前编辑中的内容当成要重跑的内容。
 *
 * 所以：开窗时定格一份，执行只读这份快照；要重跑新内容再重新定格。
 * 弹窗内的删卷/含卷勾选是**用户在弹窗里当场做的选择**，不属于外来的
 * live props，直接用 ref 读取是正确的。
 */
const snapshot = ref<{
  content: string | undefined
  envContent: string | undefined
  version: number | undefined
  expectedConfigHash: string | undefined
  create: boolean
} | null>(null)

/** 定格当前 props 作为本次提交内容 */
function takeSnapshot(): void {
  snapshot.value = {
    content: props.content,
    envContent: props.envContent,
    version: props.version,
    expectedConfigHash: props.expectedConfigHash,
    create: props.create
  }
}

const runner = useStackOperationTask({
  /*
   * 只读快照，不读 live props：确认的与实际提交的必须是同一份内容。
   * 快照缺失时退回 props 是安全的（说明尚未开窗，此时 props 即当前值）。
   */
  start: (requestId) => {
    const snap = snapshot.value ?? {
      content: props.content,
      envContent: props.envContent,
      version: props.version,
      expectedConfigHash: props.expectedConfigHash,
      create: props.create
    }
    return startStackOperation(props.agentId, props.stackName, props.action, requestId, {
      removeVolumes: props.action === 'remove' ? removeVolumesLocal.value : undefined,
      includeVolumes: props.action === 'backup' ? includeVolumesLocal.value : undefined,
      content: snap.content,
      envContent: snap.envContent,
      version: snap.version,
      expectedConfigHash: snap.expectedConfigHash,
      create: snap.create ? true : undefined
    })
  },
  fetch: (taskId, after) => getStackOperationTask(props.agentId, props.stackName, taskId, after),
  expectedName: props.stackName,
  // 同一编排可能先后跑 pull / rebuild，日志绝不能串动作
  expectedAction: props.action,
  /*
   * 只有 up 带内容（保存并部署）或新建才会把配置写进 Hub。
   * 其余动作的 503 只是"不知道有没有跑"，不能说成"配置已保存"。
   */
  savesConfig: props.create || (props.action === 'up' && props.content !== undefined)
})

const {
  task,
  output,
  segments,
  commandCount,
  dropped,
  droppedReasons,
  phase,
  starting,
  uncertain,
  permanentError,
  savedNotDeployed,
  errorText
} = runner

const logRef = ref<HTMLElement | null>(null)
const atBottom = ref(true)

/**
 * 最后一个事件的 seq。
 *
 * 自动滚动不能 watch `output.length`：达到条数上限后长度恒定不变，
 * 新内容一直进来但长度不动，滚动就彻底失效。seq 单调递增，是可靠信号。
 */
const lastSeq = computed(() => {
  const list = output.value
  return list.length === 0 ? 0 : list[list.length - 1].seq
})

/** 终态后重跑需确认 */
const showNewTaskConfirm = ref(false)
/** 首次打开时是否需要用户点按钮（危险动作由父级确认后传 autoStart） */
const awaitingStart = ref(false)
/** autoStart 是否已触发过：避免重开/重挂载重复自动开始 */
let autoStarted = false

const phaseText = computed(() => {
  switch (phase.value) {
    case 'starting':
      return '正在提交任务…'
    case 'queued':
      return '已排队，等待开始'
    case 'running':
      // 不说「第几步/共几步」——命令数是已发生的计数，不是总进度
      return commandCount.value > 1 ? '正在执行（多步）…' : '正在执行…'
    case 'done':
      return '已完成'
    case 'failed':
      return '执行失败'
    case 'interrupted':
      return '连接中断'
    case 'blocked':
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

/** 退出码：0 也要显示（成功是有意义的证据） */
const exitText = computed(() => {
  const code = task.value?.exit_code
  if (code === null || code === undefined) return ''
  return `退出码 ${String(code)}`
})

/** 备份产物路径 */
const backupPath = computed(() =>
  task.value !== null && task.value.action === 'backup' ? task.value.backup_path : ''
)

const emptyHint = computed(() => {
  if (task.value === null) {
    return awaitingStart.value ? `点击「${meta.value.label}」开始执行。` : '任务尚未开始。'
  }
  if (output.value.length === 0) return '任务已创建，等待输出…'
  return ''
})

/**
 * 上报"该动作是否占用编排"。
 *
 * 父级用它禁用其它动作按钮——同一编排并行跑两个写操作会互相踩。
 * 只把 starting/queued/running 视为占用：终态不占用，用户可以立刻做别的。
 */
watch(
  () => phase.value,
  (next) => {
    emit('busy', next === 'starting' || next === 'queued' || next === 'running')
  },
  { immediate: true }
)

async function scrollToBottom(): Promise<void> {
  await nextTick()
  const el = logRef.value
  if (el === null) return
  el.scrollTop = el.scrollHeight
}

/** 记录是否在底部（24px 容差，避免取整误差） */
function handleScroll(): void {
  const el = logRef.value
  if (el === null) return
  atBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight <= 24
}

watch(lastSeq, () => {
  if (atBottom.value) void scrollToBottom()
})

/**
 * 打开弹窗的入口逻辑（供 watch 与 onMounted 共用）。
 *
 * 决策本身是纯函数 `decideEntry`，这里只把结果映射成动作——
 * 生命周期时序（mount 时 show 已是 true、重开、重试）最容易出错，
 * 所以判断逻辑单独可测，组件不重复实现一遍。
 */
function onShow(): void {
  if (!props.show) return
  atBottom.value = true
  void scrollToBottom()

  const action = decideEntry({
    hasTask: task.value !== null,
    taskFinished: runner.finished.value,
    startAttempted: runner.hasStartAttempted(),
    autoStart: props.autoStart,
    autoStarted
  })

  /*
   * 只有全新任务才重新定格快照。
   * 已有任务/状态未知沿用原快照——否则同一个 request_id 会带新 payload，
   * Hub 幂等校验判定内容不一致，直接 409。
   */
  if (shouldSnapshot(action)) {
    takeSnapshot()
    removeVolumesLocal.value = props.removeVolumes
    includeVolumesLocal.value = props.includeVolumes
  }

  switch (action) {
    case 'resume':
      awaitingStart.value = false
      void runner.resume()
      return
    case 'retry-same-key':
      // 状态未知：等用户点「重试确认任务」，不自动重发
      awaitingStart.value = false
      return
    case 'start-new':
      awaitingStart.value = false
      autoStarted = true
      void runner.start()
      return
    case 'await-user':
      awaitingStart.value = true
      return
    case 'do-nothing':
      awaitingStart.value = false
      return
  }
}

/*
 * immediate 是必须的：父级用常驻实例 + :show 绑定，
 * 切服务器/切编排后重建实例时 show 初始值就是 true，
 * 非 immediate 的 watch 永远不会触发，autoStart 等于失效。
 * （watch 依赖的 ref 都在上方定义完毕，初始化顺序安全。）
 */
watch(() => props.show, onShow, { immediate: true })

onMounted(() => {
  void scrollToBottom()
})

/**
 * 任务进入终态时通知父级。
 *
 * **成功与失败都要 emit**：保存并部署失败时配置可能已入库，
 * 父级需要从 `task.stack_version` 拿到新版本号同步乐观锁版本——
 * 只在 done 时 emit 会让这条路径永远拿不到版本，用户下次保存必然撞锁。
 *
 * 但「记录已删除」只有成功且 Hub 明确 record_removed 才成立，
 * 失败绝不触发跳转。
 */
watch(
  () => phase.value,
  (next) => {
    if (next !== 'done' && next !== 'failed') return
    /*
     * 没有任务就不通知父级。
     *
     * 503「已保存但没执行」不会产生任务，父级若把它当成终态，
     * 会解除「状态未知」的锁定，允许下一次保存换 request_id —— 那就重复部署了。
     * 这种情况只在弹窗里显示错误，保持同 key 重试。
     */
    if (task.value === null) return
    emit('finished', task.value)
    if (next === 'done' && props.action === 'remove' && isConfirmedRemove(task.value)) {
      emit('removed')
    }
  }
)

async function handleStart(): Promise<void> {
  if (task.value !== null && !runner.finished.value) {
    await runner.resume()
    return
  }
  if (task.value !== null && runner.finished.value) {
    showNewTaskConfirm.value = true
    return
  }
  awaitingStart.value = false
  await runner.start()
}

async function confirmNewTask(): Promise<void> {
  showNewTaskConfirm.value = false
  /*
   * reset 可能被拒：上一个任务状态不明确时清空会换新 request_id，
   * 等于放弃幂等。这种情况改为重试确认，不起新任务。
   */
  if (!runner.reset()) {
    await runner.resume()
    return
  }
  // 重跑是"用现在的内容再来一次"，此刻重新定格才是对的
  takeSnapshot()
  removeVolumesLocal.value = props.removeVolumes
  includeVolumesLocal.value = props.includeVolumes
  awaitingStart.value = false
  await runner.start()
}

async function handleRetryConfirm(): Promise<void> {
  await runner.start()
}

async function handleResume(): Promise<void> {
  await runner.resume()
}

function close(): void {
  // pause 而非 dispose：关窗只是不看，还要能重开接着读
  runner.pause()
  emit('update:show', false)
}

/**
 * 是否显示「开始」按钮。
 *
 * 排除 uncertain：那种情况任务可能已在服务器上建好，显示「开始」会误导。
 */
const canStart = computed(
  () =>
    props.online &&
    !starting.value &&
    phase.value !== 'starting' &&
    !uncertain.value &&
    (task.value === null || runner.finished.value)
)

const logLabel = computed(() => `「${props.stackName}」${meta.value.label}的执行输出`)
</script>

<template>
  <n-modal
    :show="props.show"
    preset="card"
    :title="`${meta.label} · ${props.stackName}`"
    class="op-modal"
    :bordered="false"
    :mask-closable="false"
    @update:show="(v: boolean) => { if (!v) close() }"
  >
    <!-- 动作说明：明确这个动作会做什么、不会做什么 -->
    <n-alert type="info" :bordered="false" class="op-modal__alert">
      {{ meta.description }}
    </n-alert>

    <n-alert v-if="!props.online && task === null" type="warning" :bordered="false" class="op-modal__alert">
      服务器当前离线，无法执行该操作。恢复在线后可以再试。
    </n-alert>

    <!-- 删除选项：删卷是不可逆的，勾选必须在弹窗里可见可控 -->
    <div v-if="props.action === 'remove' && task === null" class="op-modal__option">
      <n-checkbox v-model:checked="removeVolumesLocal">
        同时删除数据卷（永久丢失且无法恢复）
      </n-checkbox>
    </div>

    <!-- 备份选项 -->
    <div v-if="props.action === 'backup' && task === null" class="op-modal__option">
      <n-checkbox v-model:checked="includeVolumesLocal">包含数据卷</n-checkbox>
    </div>

    <!-- 保存并部署：说清是保存 + 部署，不是只存草稿 -->
    <n-alert
      v-if="props.content !== undefined && task === null"
      type="warning"
      :bordered="false"
      class="op-modal__alert"
    >
      将先保存配置再立即部署（compose up），可能创建或重建容器。这不是草稿保存。
    </n-alert>

    <div v-if="task !== null" class="op-modal__command">
      <span class="op-modal__command-label">
        当前命令<template v-if="commandCount > 0">（已执行 {{ commandCount }} 条命令）</template>
      </span>
      <code class="op-modal__command-text">{{ task.command }}</code>
      <span v-if="task.working_dir !== ''" class="op-modal__workdir">
        工作目录 {{ task.working_dir }}
      </span>
    </div>

    <div class="op-modal__status">
      <n-tag size="small" :bordered="false" :type="phaseType">{{ phaseText }}</n-tag>
      <span v-if="exitText !== ''" class="op-modal__exit">{{ exitText }}</span>
      <!-- 只在真正执行时转圈：queued 转圈会让人以为已经在跑 -->
      <n-spin v-if="starting || phase === 'running'" size="small" />
    </div>

    <!-- 备份产物路径 -->
    <div v-if="backupPath !== ''" class="op-modal__backup">
      备份已保存至 <code>{{ backupPath }}</code>
    </div>

    <!--
      错误分档，三档互斥：
      1. uncertain / interrupted：读取或启动结果不确定，给重试入口；
      2. 启动被明确拒绝（403/409/503）：任务没建出来，必须把原因说出来；
      3. 任务自身 error：真正的执行失败。
    -->
    <n-alert v-if="uncertain" type="warning" :bordered="false" class="op-modal__alert">
      {{ errorText }}
    </n-alert>
    <!-- 配置已保存但未执行：必须说清"已保存"，否则用户会以为白写了 -->
    <n-alert
      v-else-if="savedNotDeployed"
      type="warning"
      :bordered="false"
      class="op-modal__alert"
    >
      {{ errorText }}
      <br />
      配置已经保存在 Hub 上，不需要重新编辑；服务器恢复在线后重试执行即可。
    </n-alert>
    <n-alert
      v-else-if="phase === 'interrupted'"
      type="warning"
      :bordered="false"
      class="op-modal__alert"
    >
      {{ errorText }}
      <n-button text size="tiny" class="op-modal__retry" @click="handleResume">
        {{ permanentError ? '重新读取' : '立即重试' }}
      </n-button>
    </n-alert>
    <n-alert
      v-else-if="errorText !== '' && (task === null || task.error === '')"
      type="error"
      :bordered="false"
      class="op-modal__alert"
    >
      {{ errorText }}
    </n-alert>

    <n-alert
      v-if="task !== null && task.error !== ''"
      type="error"
      :bordered="false"
      class="op-modal__alert"
    >
      {{ task.error }}
    </n-alert>

    <div v-if="dropped" class="op-modal__truncated">
      <span v-if="droppedReasons.includes('trimmed')">
        输出过长，较早的内容已省略（仅保留最近 256 KB / 2000 条）。
      </span>
      <span v-if="droppedReasons.includes('server')">
        服务器端的输出已被截断，完整日志请到服务器查看。
      </span>
      <span v-if="droppedReasons.includes('gap')">日志中间有缺失（部分输出未能接收）。</span>
    </div>

    <!--
      分段渲染：command 事件作为步骤标题，后面跟该步输出。
      文本一律插值（不经 v-html），且已剥 ANSI 转义。
    -->
    <div
      ref="logRef"
      class="op-modal__log"
      role="log"
      aria-live="polite"
      :aria-label="logLabel"
      @scroll="handleScroll"
    >
      <div v-if="segments.length === 0" class="op-modal__empty">{{ emptyHint }}</div>
      <template v-else>
        <div v-for="(seg, i) in segments" :key="`seg-${String(i)}`" class="op-modal__segment">
          <div v-if="seg.command !== null" class="op-modal__step">
            <span class="op-modal__step-no">第 {{ i + 1 }} 步</span>
            <code class="op-modal__step-cmd">{{ seg.command }}</code>
          </div>
          <!--
            stage 事件是阶段进度说明（如备份复制文件），没有对应命令：
            用单独的弱化样式显示，绝不加 `$` 前缀——那会让人以为执行了程序。
          -->
          <div
            v-for="sc in seg.chunks.filter((c) => c.stream === 'stage')"
            :key="`stage-${String(sc.seq)}`"
            class="op-modal__stage"
          >
            {{ sc.text }}
          </div>
          <pre v-if="seg.chunks.some((c) => c.stream !== 'stage')" class="op-modal__lines">{{
            seg.chunks
              .filter((c) => c.stream !== 'stage')
              .map((c) => c.text)
              .join('\n')
          }}</pre>
        </div>
      </template>
    </div>

    <div class="op-modal__footer">
      <n-button
        v-if="canStart"
        type="primary"
        size="small"
        :loading="starting"
        @click="handleStart"
      >
        {{ task === null ? `开始${meta.label}` : `重新执行${meta.label}` }}
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
      <span v-else-if="task !== null && !runner.finished.value" class="op-modal__hint">
        关闭这个窗口不会停止任务，任务会继续在服务器上执行。
      </span>
      <n-button size="small" @click="close">
        {{ runner.finished.value || task === null ? '关闭' : '在后台继续' }}
      </n-button>
    </div>

    <n-modal
      v-model:show="showNewTaskConfirm"
      preset="dialog"
      type="warning"
      :title="`确认重新执行${meta.label}？`"
      :positive-text="`重新执行`"
      negative-text="取消"
      @positive-click="confirmNewTask"
    >
      上一次执行已经结束。重新执行会创建一个新任务，并清空当前窗口里的日志。
    </n-modal>
  </n-modal>
</template>

<style scoped>
.op-modal__alert {
  margin-bottom: 10px;
}

.op-modal__option {
  margin-bottom: 10px;
}

.op-modal__command {
  display: flex;
  flex-direction: column;
  gap: 4px;
  margin-bottom: 10px;
}

.op-modal__command-label {
  color: var(--arkade-text-tertiary);
  font-size: 12px;
}

.op-modal__command-text {
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

.op-modal__workdir {
  color: var(--arkade-text-tertiary);
  font-size: 12px;
  word-break: break-all;
}

.op-modal__status {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 8px;
}

.op-modal__exit {
  color: var(--arkade-text-tertiary);
  font-size: 12px;
}

.op-modal__backup {
  margin-bottom: 8px;
  font-size: 12px;
  color: var(--arkade-text-secondary);
  word-break: break-all;
}

.op-modal__truncated {
  color: var(--arkade-text-tertiary);
  font-size: 12px;
  margin-bottom: 6px;
}

/* 固定高度 + 内滚动：长日志不会把弹窗撑爆 */
.op-modal__log {
  height: 320px;
  margin: 0 0 12px;
  padding: 8px 10px;
  overflow: auto;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.55;
  color: var(--arkade-text-primary);
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
  border-radius: 4px;
}

.op-modal__segment + .op-modal__segment {
  margin-top: 6px;
}

/* 步骤标题与普通输出区分开，一眼看出多命令在逐步执行 */
.op-modal__step {
  display: flex;
  align-items: baseline;
  gap: 6px;
  padding: 2px 0;
  border-top: 1px dashed var(--arkade-border-color);
  margin-top: 6px;
}

.op-modal__segment:first-child .op-modal__step {
  border-top: none;
  margin-top: 0;
}

.op-modal__step-no {
  flex-shrink: 0;
  color: var(--arkade-text-tertiary);
  font-size: 11px;
}

.op-modal__step-cmd {
  color: var(--arkade-link-color, #2080f0);
  word-break: break-all;
}

.op-modal__lines {
  margin: 0;
  white-space: pre-wrap;
  word-break: break-all;
  font-family: inherit;
}

/*
 * 阶段进度行：弱化、不占命令样式。
 * 没有百分比——stage 事件本身不带进度数值，画假进度条会误导。
 */
.op-modal__stage {
  color: var(--arkade-text-tertiary);
  font-style: italic;
  padding: 1px 0;
}

.op-modal__empty {
  color: var(--arkade-text-tertiary);
}

.op-modal__footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
}

.op-modal__hint {
  color: var(--arkade-text-tertiary);
  font-size: 12px;
}

.op-modal__retry {
  margin-left: 6px;
}
</style>
