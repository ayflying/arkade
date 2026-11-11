<script setup lang="ts">
/**
 * 迁移编排到另一台服务器（全量迁移）
 *
 * ## 为什么单独一个弹窗，而不是复用 StackOperationModal
 *
 * 迁移是「源编排 → 目标服务器」，和编辑本编排的动作不是一个维度：
 * 它需要「目标服务器」「是否含数据卷」「是否自动启动」三个专属入参，
 * 任务的 name 仍是源编排名但 action 是新值 migrate。
 * 硬塞进 `StackOperationAction` 会让所有其它动作的按钮循环、禁用判断、
 * 动作校验都被迫处理一个它们不认识的动​作。
 *
 * 但**进度展示完全复用**：任务体结构与增量读取接口和操作任务一模一样，
 * 状态机就是 `useStackOperationTask`（经 `useStackMigrateTask` 接到迁移端点）。
 *
 * ## 界面必须讲清的四件事
 *
 * 见 `STACK_MIGRATE_NOTES`：不含数据卷 / bind 挂载不迁移 /
 * 目标同名会被拒 / 复制不是移动。这四条每一条被误解都会让用户
 * 得到与预期相反的结果，所以不藏进帮助文档。
 */
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { NAlert, NButton, NCheckbox, NModal, NSelect, NSpin, NTag } from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { useStackMigrateTask } from '@/composables/useStackMigrateTask'
import type { StackMigrateTarget } from '@/types/stackMigrate'
import { STACK_MIGRATE_NOTES } from '@/types/stackOperationTask'
import { useAgentsStore } from '@/stores/agents'

const props = withDefaults(
  defineProps<{
    show: boolean
    /** 源服务器 id */
    agentId: string
    /** 源编排名 */
    stackName: string
    /** 源服务器是否在线 */
    online: boolean
    /** 全部服务器（含源），用于渲染目标下拉 */
    agents: StackMigrateTarget[]
  }>(),
  {}
)

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
  /** 迁移任务进入终态（成功或失败都会发，父级据此刷新） */
  (e: 'finished'): void
  /** 该动作是否正在执行（父级据此锁住其它动作） */
  (e: 'busy', value: boolean): void
}>()

const targetAgentId = ref('')
/**
 * 两个勾选**默认都不勾**。
 *
 * 都是「做了会带来额外后果」的选择：不带卷目标上是空的，
 * 带卷会传大量数据；自动启动会在目标上直接跑起容器。
 * 让用户显式选择，比替他决定安全。
 */
const includeVolumes = ref(false)
const startAfter = ref(false)

const runner = useStackMigrateTask(
  { agentId: props.agentId, stackName: props.stackName },
  {
    targetAgentId: () => targetAgentId.value,
    includeVolumes: () => includeVolumes.value,
    startAfter: () => startAfter.value
  }
)

const {
  task,
  segments,
  commandCount,
  phase,
  starting,
  uncertain,
  permanentError,
  errorText,
  dropped,
  droppedReasons,
  notDeployed
} = runner

/** 目标候选：排除源服务器本身（迁移到自己没有意义） */
const options = computed<SelectOption[]>(() =>
  props.agents
    .filter((a) => a.id !== props.agentId)
    .map((a) => ({
      label: a.online ? a.name : `${a.name}（离线）`,
      value: a.id,
      // 离线目标不可选：后端也会拒绝，但先让用户看到而不是点了才报错
      disabled: !a.online
    }))
)

/** 是否正在刷新服务器列表。
 *
 * 必须和「确实没有其它服务器」区分开：空态显示成「暂无其它服务器」
 * 会让用户以为功能坏了（实际只是还没加载出来），而 loading 至少说明在建。
 */
const agentsStore = useAgentsStore()
const refreshingAgents = ref(false)
/** 其它服务器数量（不含源）：用于判断空态 */
const otherAgentCount = computed(() => options.value.length)

/** 选中的目标服务器名，用于摘要展示 */
const targetName = computed(() => {
  const hit = props.agents.find((a) => a.id === targetAgentId.value)
  return hit === undefined ? '' : hit.name
})

/** 执行期间锁定选择：中途改目标等于让任务执行的东西和界面显示的不一致 */
const locked = computed(() => task.value !== null || starting.value)

const canStart = computed(
  () =>
    props.online &&
    !starting.value &&
    !uncertain.value &&
    !locked.value &&
    targetAgentId.value !== '' &&
    targetAgentId.value !== props.agentId
)

const phaseType = computed<'default' | 'success' | 'error' | 'warning' | 'info'>(() => {
  switch (phase.value) {
    case 'done':
      return 'success'
    case 'failed':
      return 'error'
    case 'interrupted':
    case 'blocked':
      return 'warning'
    case 'running':
    case 'queued':
      return 'info'
    default:
      return 'default'
  }
})

const phaseText = computed(() => {
  switch (phase.value) {
    case 'idle':
      return '尚未开始'
    case 'starting':
      return '正在提交…'
    case 'queued':
      return '已排队'
    case 'running':
      return commandCount.value > 1 ? '正在执行（多步）…' : '正在执行…'
    case 'done':
      return '迁移完成'
    case 'failed':
      return '迁移失败'
    case 'interrupted':
      return '连接中断'
    case 'blocked':
      return '状态未知'
    default:
      return '—'
  }
})

const exitText = computed(() => {
  const t = task.value
  if (t === null || t.exit_code === null) return ''
  return `退出码 ${String(t.exit_code)}`
})

const emptyHint = computed(() => {
  if (task.value === null) return '还没有开始迁移。'
  return '正在等待服务器输出…'
})

/* ───────── 自动滚动（只在用户位于底部时跟随） ───────── */

const logRef = ref<HTMLElement | null>(null)
const atBottom = ref(true)
/** 用最后一条事件的 seq 触发滚动：output.length 在有去重/裁剪时不可靠 */
const lastSeq = computed(() =>
  segments.value.length === 0 ? 0 : (segments.value.at(-1)?.chunks.at(-1)?.seq ?? 0)
)

async function scrollToBottom(): Promise<void> {
  await nextTick()
  const el = logRef.value
  if (el !== null) el.scrollTop = el.scrollHeight
}

function handleScroll(): void {
  const el = logRef.value
  if (el === null) return
  atBottom.value = el.scrollHeight - el.scrollTop - el.clientHeight < 24
}

watch(lastSeq, () => {
  if (atBottom.value) void scrollToBottom()
})

/* ───────── 生命周期 ───────── */

/**
 * 上报「是否有任务在执行」，父级用它锁住其它动作。
 *
 * 迁移是重操作，不允许和启动/重建/删除同时跑——两个动作会争抢同一份编排。
 */
watch(
  () => phase.value,
  (next) => {
    emit('busy', next === 'starting' || next === 'queued' || next === 'running')
  },
  { immediate: true }
)

/** 终态通知父级刷新（成功失败都要，列表状态可能都变了） */
watch(
  () => phase.value,
  (next) => {
    if (next !== 'done' && next !== 'failed') return
    emit('finished')
  }
)

/** 开窗入口：已有任务继续看，没有等用户点开始 */
function onShow(): void {
  if (!props.show) return
  atBottom.value = true
  void scrollToBottom()
  void ensureAgents()
  if (task.value !== null && !runner.finished.value) {
    // 关窗再打开：接着读，绝不重新提交
    void runner.resume()
  }
}

/**
 * 兜底加载服务器列表。
 *
 * 详情页可能被直接打开（没经过列表页），此时全局 store 还没数据，
 * 下拉会是空的——用户会理解成「没有其它服务器可迁移」，把功能当成坏的。
 *
 * 失败**静默忽略**：迁移弹窗的主职责是发起迁移，不是汇报列表加载失败；
 * 弹出一条"加载服务器列表失败"只会让用户困惑，而且现有列表仍可用于选择。
 */
async function ensureAgents(): Promise<void> {
  if (agentsStore.agents.length > 0) return
  refreshingAgents.value = true
  try {
    await agentsStore.refresh(true)
  } catch {
    // 静默：列表为空时下拉区会显示明确提示，不需要额外报错
  } finally {
    refreshingAgents.value = false
  }
}

/*
 * immediate：父级用常驻实例 + :show 绑定，组件创建时 show 可能已是 true，
 * 非 immediate 的 watch 不会触发，重开就不会 resume。
 */
watch(() => props.show, onShow, { immediate: true })

onMounted(() => {
  void scrollToBottom()
})

async function handleStart(): Promise<void> {
  if (!canStart.value) return
  atBottom.value = true
  await runner.start()
}

async function handleResume(): Promise<void> {
  await runner.resume()
}

/**
 * 关闭：只暂停轮询，**任务在服务器上继续跑**。
 *
 * 迁移是长任务，关窗不是取消——既不该真的中止执行，也不该让用户以为
 * 自己刚刚取消掉了。所以关闭按钮的文案会随状态变化（执行中显示
 * 「关闭窗口（迁移继续）」），关窗后再点「迁移」按钮能打开同一个任务继续看。
 */
function close(): void {
  runner.pause()
  emit('update:show', false)
}
</script>

<template>
  <n-modal
    :show="props.show"
    preset="card"
    :title="`迁移编排 · ${props.stackName}`"
    class="migrate-modal"
    :bordered="false"
    :mask-closable="false"
    @update:show="(v: boolean) => { if (!v) close() }"
  >
    <n-alert v-if="!props.online && task === null" type="warning" :bordered="false" class="migrate-modal__alert">
      源服务器当前离线，无法发起迁移。恢复在线后可以再试。
    </n-alert>

    <!-- 后端还没部署迁移接口：这是「功能未就绪」，不是失败 -->
    <n-alert v-if="notDeployed" type="info" :bordered="false" class="migrate-modal__alert">
      迁移功能的后端接口尚未部署，暂时无法使用。请联系管理员升级 Hub 与 Agent 后再试。
    </n-alert>

    <!-- 目标服务器 -->
    <div class="migrate-modal__row">
      <span class="migrate-modal__label">目标服务器</span>
      <n-select
        v-model:value="targetAgentId"
        class="migrate-modal__select"
        :options="options"
        :placeholder="refreshingAgents ? '正在加载服务器列表…' : '选择要把编排复制到哪台服务器'"
        :loading="refreshingAgents"
        :disabled="locked"
        filterable
      />
    </div>
    <!-- 加载中与真正为空必须分开说，否则空态会被当成"没有可选目标" -->
    <p v-if="refreshingAgents" class="migrate-modal__hint migrate-modal__hint-gap">
      正在加载服务器列表…
    </p>
    <p
      v-else-if="otherAgentCount === 0"
      class="migrate-modal__hint migrate-modal__hint-gap"
    >
      没有其它可选的服务器。迁移目标是另一台已接入的服务器，请先在「服务器」页添加。
    </p>

    <!-- 两个勾选：默认都不勾，各带小字说明 -->
    <div class="migrate-modal__options">
      <n-checkbox v-model:checked="includeVolumes" :disabled="locked">
        同时迁移数据卷
      </n-checkbox>
      <!--
        未勾选时**必须**明说目标上数据卷是空的。
        这是本次需求的核心差别：用户看到"迁移"两个字，天然以为数据也过去了，
        不勾选就得到一台容器能跑但数据全无的服务器——必须在事前讲明白，
        不能只在勾选时才有说明。
      -->
      <p v-if="includeVolumes" class="migrate-modal__hint">
        将把数据卷内容一并复制到目标服务器。数据量大时迁移会明显变慢。
      </p>
      <p v-else class="migrate-modal__hint migrate-modal__hint-warn">
        <strong>目标服务器上的数据卷是空的。</strong>
        默认只迁移编排配置和用到的镜像，容器起来后看不到原来的数据。
        需要带上数据请勾选上方选项。
      </p>

      <n-checkbox v-model:checked="startAfter" :disabled="locked" class="migrate-modal__check-gap">
        迁移完成后自动启动编排
      </n-checkbox>
      <p v-if="startAfter" class="migrate-modal__hint">
        迁移完成后会在目标服务器上执行启动，容器会立刻运行。
      </p>
      <p v-else class="migrate-modal__hint">
        不勾选时迁移完成后目标服务器上只有配置，容器不会运行，需要你手动启动。
      </p>
    </div>

    <!-- 必须讲清的四件事 -->
    <n-alert type="info" :bordered="false" class="migrate-modal__notes">
      <ul class="migrate-modal__note-list">
        <li v-for="(note, i) in STACK_MIGRATE_NOTES" :key="`note-${String(i)}`">{{ note }}</li>
      </ul>
    </n-alert>

    <!-- 执行中显示目标，避免用户忘记选的是哪台 -->
    <div v-if="task !== null" class="migrate-modal__summary">
      目标服务器：<strong>{{ targetName === '' ? '—' : targetName }}</strong>
      <span class="migrate-modal__summary-sep">·</span>
      {{ includeVolumes ? '含数据卷' : '不含数据卷' }}
      <span class="migrate-modal__summary-sep">·</span>
      {{ startAfter ? '完成后自动启动' : '完成后不启动' }}
    </div>

    <div v-if="task !== null && task.command !== ''" class="migrate-modal__command">
      <span class="migrate-modal__command-label">
        当前命令<template v-if="commandCount > 0">（已执行 {{ commandCount }} 条命令）</template>
      </span>
      <code class="migrate-modal__command-text">{{ task.command }}</code>
    </div>

    <div class="migrate-modal__status">
      <n-tag size="small" :bordered="false" :type="phaseType">{{ phaseText }}</n-tag>
      <span v-if="exitText !== ''" class="migrate-modal__exit">{{ exitText }}</span>
      <!-- 只在真正执行时转圈：queued 转圈会让人以为已经在跑 -->
      <n-spin v-if="starting || phase === 'running'" size="small" />
    </div>

    <!-- 错误分档：不确定 / 中断给重试；明确失败给原因 -->
    <n-alert v-if="uncertain" type="warning" :bordered="false" class="migrate-modal__alert">
      {{ errorText }}
    </n-alert>
    <n-alert
      v-else-if="phase === 'interrupted'"
      type="warning"
      :bordered="false"
      class="migrate-modal__alert"
    >
      {{ errorText }}
      <n-button text size="tiny" class="migrate-modal__retry" @click="handleResume">
        {{ permanentError ? '重新读取' : '立即重试' }}
      </n-button>
    </n-alert>
    <n-alert
      v-else-if="errorText !== '' && !notDeployed && (task === null || task.error === '')"
      type="error"
      :bordered="false"
      class="migrate-modal__alert"
    >
      {{ errorText }}
    </n-alert>

    <n-alert
      v-if="task !== null && task.error !== ''"
      type="error"
      :bordered="false"
      class="migrate-modal__alert"
    >
      {{ task.error }}
    </n-alert>

    <div v-if="dropped" class="migrate-modal__truncated">
      <span v-if="droppedReasons.includes('trimmed')">
        输出过长，较早的内容已省略（仅保留最近 256 KB / 2000 条）。
      </span>
      <span v-if="droppedReasons.includes('server')">
        服务器端的输出已被截断，完整日志请到服务器查看。
      </span>
      <span v-if="droppedReasons.includes('gap')">日志中间有缺失（部分输出未能接收）。</span>
    </div>

    <!--
      日志区：与 StackOperationModal 同一套分段渲染。
      command 事件作为步骤标题，stage 是阶段进度（不加 $ 前缀），
      文本一律插值不经 v-html，且已剥 ANSI 转义。
    -->
    <div
      ref="logRef"
      class="migrate-modal__log"
      role="log"
      aria-live="polite"
      aria-label="迁移执行日志"
      @scroll="handleScroll"
    >
      <div v-if="segments.length === 0" class="migrate-modal__empty">{{ emptyHint }}</div>
      <template v-else>
        <div v-for="(seg, i) in segments" :key="`seg-${String(i)}`" class="migrate-modal__segment">
          <div v-if="seg.command !== null" class="migrate-modal__step">
            <span class="migrate-modal__step-no">第 {{ i + 1 }} 步</span>
            <code class="migrate-modal__step-cmd">{{ seg.command }}</code>
          </div>
          <div
            v-for="sc in seg.chunks.filter((c) => c.stream === 'stage')"
            :key="`stage-${String(sc.seq)}`"
            class="migrate-modal__stage"
          >
            {{ sc.text }}
          </div>
          <pre v-if="seg.chunks.some((c) => c.stream !== 'stage')" class="migrate-modal__lines">{{
            seg.chunks
              .filter((c) => c.stream !== 'stage')
              .map((c) => c.text)
              .join('\n')
          }}</pre>
        </div>
      </template>
    </div>

    <div class="migrate-modal__footer">
      <n-button
        v-if="canStart"
        type="primary"
        size="small"
        :loading="starting"
        @click="handleStart"
      >
        开始迁移
      </n-button>
      <!-- 状态未知：给同 key 重试入口，不换任务 -->
      <n-button
        v-else-if="!locked && (uncertain || (task === null && phase === 'blocked'))"
        type="primary"
        size="small"
        :loading="starting"
        @click="handleResume"
      >
        重试确认
      </n-button>
      <span v-else-if="task === null && !props.online" class="migrate-modal__hint-footer">
        源服务器离线，暂不可用。
      </span>
      <span v-else-if="task === null" class="migrate-modal__hint-footer">
        选择目标服务器后点「开始迁移」。
      </span>
      <!--
        执行中明确说"迁移继续"，避免用户以为点关闭＝取消。
        迁移一旦开始就不会因为关窗而中止。
      -->
      <span
        v-else-if="phase === 'running' || phase === 'queued' || starting"
        class="migrate-modal__hint-footer"
      >
        关闭窗口不会中断迁移，可在「迁移」按钮重新打开查看进度。
      </span>
      <n-button size="small" @click="close">
        {{ phase === 'running' || phase === 'queued' || starting ? '关闭窗口（迁移继续）' : '关闭' }}
      </n-button>
    </div>
  </n-modal>
</template>

<style scoped>
.migrate-modal__alert {
  margin-bottom: 12px;
}

.migrate-modal__row {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 14px;
}

.migrate-modal__label {
  flex: 0 0 84px;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.migrate-modal__select {
  flex: 1 1 auto;
  min-width: 0;
}

.migrate-modal__options {
  margin-bottom: 14px;
  padding: 10px 12px;
  border: 1px solid var(--arkade-border-color);
  border-radius: 6px;
}

.migrate-modal__hint {
  margin: 6px 0 0;
  font-size: 12px;
  line-height: 1.6;
  color: var(--arkade-text-tertiary);
}

.migrate-modal__hint-gap {
  margin: -8px 0 12px;
}

/* 未含数据卷的提示要比普通说明更显眼：这是用户最容易误解成相反结论的点 */
.migrate-modal__hint-warn {
  color: var(--arkade-text-secondary);
}

.migrate-modal__check-gap {
  margin-top: 12px;
}

.migrate-modal__notes {
  margin-bottom: 12px;
  font-size: 12px;
  line-height: 1.7;
}

.migrate-modal__note-list {
  margin: 0;
  padding-left: 18px;
}

.migrate-modal__summary {
  margin-bottom: 10px;
  padding: 8px 10px;
  font-size: 12px;
  color: var(--arkade-text-secondary);
  background: var(--arkade-page-bg);
  border-radius: 6px;
}

.migrate-modal__summary-sep {
  margin: 0 6px;
  color: var(--arkade-text-tertiary);
}

.migrate-modal__command {
  display: flex;
  flex-wrap: wrap;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 8px;
  font-size: 12px;
}

.migrate-modal__command-label {
  color: var(--arkade-text-tertiary);
}

.migrate-modal__command-text {
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  color: var(--arkade-text-primary);
  word-break: break-all;
}

.migrate-modal__status {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 10px;
}

.migrate-modal__exit {
  font-size: 12px;
  font-family: var(--arkade-mono-font);
  color: var(--arkade-text-tertiary);
}

.migrate-modal__retry {
  margin-left: 8px;
}

.migrate-modal__truncated {
  margin-bottom: 8px;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.migrate-modal__truncated > span + span {
  margin-left: 8px;
}

.migrate-modal__log {
  height: 260px;
  overflow: auto;
  padding: 10px 12px;
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
  border-radius: 6px;
  font-size: 12px;
}

.migrate-modal__empty {
  color: var(--arkade-text-tertiary);
}

.migrate-modal__segment + .migrate-modal__segment {
  margin-top: 10px;
}

.migrate-modal__step {
  display: flex;
  align-items: baseline;
  gap: 8px;
  margin-bottom: 4px;
  font-size: 11px;
  color: var(--arkade-text-tertiary);
}

.migrate-modal__step-no {
  flex: 0 0 auto;
}

.migrate-modal__step-cmd {
  font-family: var(--arkade-mono-font);
  font-size: 11px;
  color: var(--arkade-text-secondary);
  word-break: break-all;
}

.migrate-modal__stage {
  margin: 2px 0;
  font-style: italic;
  color: var(--arkade-text-tertiary);
}

.migrate-modal__lines {
  margin: 0;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.55;
  color: var(--arkade-text-primary);
  white-space: pre-wrap;
  word-break: break-all;
}

.migrate-modal__footer {
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: 8px;
  margin-top: 14px;
}

.migrate-modal__hint-footer {
  margin-right: auto;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}
</style>