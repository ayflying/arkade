<script setup lang="ts">
/**
 * 编排操作按钮组
 *
 * 所有编辑动作都通过 StackOperationModal 执行：弹窗里展示真实命令与实时输出，
 * 不再用「同步调用接口 + 一个 toast」的黑盒方式——用户看不到到底跑了什么。
 *
 * ## 每个动作一个常驻弹窗实例（关键设计）
 *
 * 起初只渲染「当前动作」的一个弹窗，关闭时把 activeAction 置空。
 * 那会导致弹窗被 unmount，**里面正在进行/已有结果的任务实例一起被销毁**：
 * 用户关掉窗口再打开，组件重新挂载 → 新 request_id → 后端起第二个任务。
 * 对删除、重建这类危险动作，这就是重复执行。
 *
 * 所以改成：每个动作都常驻一个实例，用 `show` 控制可见性。
 * - 关闭 = 只 `pause()` 轮询（任务继续在服务器跑），实例与 request_id 都留着；
 * - 重开同动作 = 原实例 resume()，绝不重新 POST；
 * - 切到别的动作 = 旧实例的状态原样保留，切回来还在。
 *
 * `key` 绑定 agentId/name/action：换服务器或换编排才重建实例（那本来就该是新上下文）。
 *
 * ## 有任务在跑时限制冲突操作
 *
 * 一个编排同时跑两个写操作（比如重建还没结束又去删除）只会互相踩。
 * 有动作进行中时，其它动作按钮明确禁用，而不是让用户排队等一个不存在的队列。
 */
import { computed, onUnmounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton } from 'naive-ui'
import StackOperationModal from './StackOperationModal.vue'
import type { StackOperationAction } from '@/types/stackOperationTask'
import { STACK_OPERATION_ACTIONS, STACK_OPERATION_META } from '@/types/stackOperationTask'
import { confirmAction, confirmWithOption } from '@/utils/confirm'

const props = defineProps<{
  agentId: string
  stackName: string
  online: boolean
  busy: boolean
  unsynced: boolean
}>()
const emit = defineEmits<{ changed: []; removed: [] }>()

const router = useRouter()
const running = ref(false)
let alive = true
onUnmounted(() => { alive = false })

/**
 * 按钮展示顺序：删除单独放最后（红色、危险）。
 * 其余按「常用 → 破坏性递增」排。
 */
const BUTTONS: StackOperationAction[] = ['up', 'stop', 'restart', 'pull', 'rebuild', 'backup']

/** 未同步时不能启动/重建：会把未登记的内容当成已登记来执行 */
const UNSYNCED_BLOCKED: StackOperationAction[] = ['up', 'rebuild']

/** 弹窗可见性：记录哪个动作当前被打开（null = 都关着） */
const activeAction = ref<StackOperationAction | null>(null)
const modalShow = ref(false)
const autoStart = ref(false)
const removeVolumes = ref(false)

/**
 * 每个动作的任务是否仍在执行中。
 *
 * 由各自弹窗实例上报——任务实例在弹窗内部，父级只关心"是否占用该编排"。
 */
const busyActions = reactive(new Set<StackOperationAction>())

const anyBusy = computed(() => busyActions.size > 0)

/**
 * 某动作按钮是否禁用。
 *
 * 有任务执行中时只允许打开**同一个**动作查看进度；
 * 其它动作一律禁用——同时跑两个写操作会互相破坏，
 * 而"排队等待"是我们没有真实队列支撑的假承诺。
 */
function disabledFor(action: StackOperationAction): boolean {
  if (!props.online || props.busy || running.value) return true
  if (props.unsynced && UNSYNCED_BLOCKED.includes(action)) return true
  if (anyBusy.value && !busyActions.has(action) && activeAction.value !== action) return true
  return false
}

function busyHint(action: StackOperationAction): string {
  if (disabledFor(action) && anyBusy.value && !busyActions.has(action)) {
    return '已有操作正在执行，请等它结束'
  }
  return ''
}

function openModal(action: StackOperationAction, auto = false): void {
  /*
   * 打开已存在的实例：若该动作已有任务，弹窗内部会 resume() 继续看，
   * 不会重新 POST —— 这正是常驻实例的意义。
   */
  activeAction.value = action
  autoStart.value = auto
  modalShow.value = true
}

async function handleAction(action: StackOperationAction): Promise<void> {
  if (disabledFor(action)) return
  const meta = STACK_OPERATION_META[action]

  if (!meta.dangerous) {
    openModal(action)
    return
  }

  running.value = true
  try {
    const ok = await confirmAction({
      title: `确认${meta.label}编排？`,
      content: `将对「${props.stackName}」执行${meta.label}。\n\n${meta.description}`,
      positiveText: meta.label,
      danger: true
    })
    if (!ok || !alive) return
    openModal(action, true)
  } finally {
    if (alive) running.value = false
  }
}

/**
 * 删除：确认时带「同时删数据卷」勾选，勾选值**不可丢**。
 * 用户明确勾了删卷却没带上，或没勾却被删了，都是事故。
 */
async function handleRemove(): Promise<void> {
  if (disabledFor('remove')) return
  running.value = true
  try {
    const meta = STACK_OPERATION_META.remove
    const result = await confirmWithOption({
      title: '确认删除编排？',
      content: `将停止并移除「${props.stackName}」的所有容器。\n\n${meta.description}`,
      optionLabel: '同时删除数据卷（永久丢失且无法恢复）',
      optionDefault: false,
      danger: true,
      positiveText: '删除'
    })
    if (!result.confirmed || !alive) return
    removeVolumes.value = result.optionChecked
    openModal('remove', true)
  } finally {
    if (alive) running.value = false
  }
}

function handleFinished(): void {
  emit('changed')
}

/** Hub 确认记录已删除 → 通知父级并跳回列表 */
function handleRemoved(): void {
  emit('changed')
  emit('removed')
  void router.replace({ name: 'agent-stacks', params: { id: props.agentId } })
}

/** 某动作的任务执行状态变化（用于禁用冲突动作） */
function handleBusy(action: StackOperationAction, busy: boolean): void {
  if (busy) busyActions.add(action)
  else busyActions.delete(action)
}

/**
 * 弹窗关闭：**不清空 activeAction**。
 *
 * 清空会让 `activeAction === action` 的 show 条件失效，进而 unmount 实例、
 * 丢掉任务与 request_id。这里只记 modalShow=false，实例照旧存活。
 */
function handleModalShow(value: boolean): void {
  modalShow.value = value
}
</script>

<template>
  <div class="stack-actions">
    <n-button
      v-for="action in BUTTONS"
      :key="action"
      size="small"
      :disabled="disabledFor(action)"
      :title="busyHint(action)"
      @click="handleAction(action)"
    >
      {{ STACK_OPERATION_META[action].label }}
    </n-button>

    <n-button
      size="small"
      type="error"
      secondary
      :disabled="disabledFor('remove')"
      :title="busyHint('remove')"
      @click="handleRemove"
    >
      删除
    </n-button>
  </div>

  <!--
    每个动作一个常驻实例（不 v-if）：关闭只隐藏，任务实例与 request_id 保留。
    show 由 modalShow + activeAction 共同决定；key 只随 agent/编排/动作变化。
  -->
  <stack-operation-modal
    v-for="action in STACK_OPERATION_ACTIONS"
    :key="`${agentId}/${stackName}/${action}`"
    :show="modalShow && activeAction === action"
    :agent-id="agentId"
    :stack-name="stackName"
    :action="action"
    :online="online"
    :remove-volumes="removeVolumes"
    :auto-start="autoStart && activeAction === action"
    @finished="handleFinished"
    @removed="handleRemoved"
    @busy="(v: boolean) => handleBusy(action, v)"
    @update:show="handleModalShow"
  />
</template>

<style scoped>
.stack-actions { display: flex; gap: 8px; flex-wrap: wrap; }
</style>