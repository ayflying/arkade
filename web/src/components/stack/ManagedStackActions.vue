<script setup lang="ts">
import { onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NButton } from 'naive-ui'
import StackPullModal from './StackPullModal.vue'
import { deleteStack, runStackAction } from '@/api/stacks'
import type { StackAction } from '@/types/stack'
import { confirmAction, confirmWithOption } from '@/utils/confirm'
import { toastError, toastSuccess } from '@/utils/feedback'
const props = defineProps<{ agentId: string; stackName: string; online: boolean; busy: boolean; unsynced: boolean }>()
const emit = defineEmits<{ changed: [] }>()
const router = useRouter()
const running = ref(false)
/** 「更新镜像」走独立的实时输出弹窗，不再用 runStackAction('pull') */
const pullShow = ref(false)
let alive = true
onUnmounted(() => { alive = false })
const actions: { action: StackAction; label: string }[] = [
  { action: 'start', label: '启动' }, { action: 'stop', label: '停止' }, { action: 'restart', label: '重启' },
  { action: 'recreate', label: '重建' }, { action: 'backup', label: '备份' }
]
async function run(action: StackAction, label: string) {
  if (running.value || !props.online || props.busy) return
  const id = props.agentId, name = props.stackName
  running.value = true
  try {
    if (!(await confirmAction({ title: `确认${label}编排？`, content: `将对「${name}」执行${label}。启动、重启或重建可能影响服务可用性。`, positiveText: label })) || !alive) return
    await runStackAction(id, name, { action })
    if (!alive) return
    toastSuccess(`已执行「${label}」操作`); emit('changed')
  } catch (err) { if (alive) toastError(err instanceof Error ? err.message : '操作失败') }
  finally { if (alive) running.value = false }
}
/**
 * 更新镜像：直接打开弹窗。
 *
 * 弹窗内有一个明确的「开始更新镜像」按钮，那一步就是确认——
 * 不再先弹一次确认框再进来，否则按钮写着「开始」却没开始，
 * 用户要先点两次才真正执行，容易误判。
 *
 * 防误导靠弹窗里固定的说明：「只拉取镜像，不会自动重建」。
 */
function handlePull() {
  if (running.value || !props.online || props.busy) return
  pullShow.value = true
}
async function remove() {
  if (running.value || !props.online || props.busy) return
  const id = props.agentId, name = props.stackName
  running.value = true
  try {
    const result = await confirmWithOption({ title: '确认删除编排？', content: `将停止并移除「${name}」的所有容器。默认保留数据卷。`, optionLabel: '同时删除数据卷（永久丢失且无法恢复）', optionDefault: false, danger: true, positiveText: '删除' })
    if (!result.confirmed || !alive) return
    await deleteStack(id, name, result.optionChecked)
    if (!alive) return
    toastSuccess('编排已删除')
    void router.replace({ name: 'agent-stacks', params: { id } })
  } catch (err) { if (alive) toastError(err instanceof Error ? err.message : '删除失败') }
  finally { if (alive) running.value = false }
}
</script>
<template>
  <div class="stack-actions">
    <n-button v-for="item in actions" :key="item.action" size="small" :disabled="!online || busy || running || (unsynced && ['start', 'recreate'].includes(item.action))" @click="run(item.action, item.label)">{{ item.label }}</n-button>
    <!-- 更新镜像：独立弹窗展示真实命令输出，不走 runStackAction -->
    <n-button size="small" :disabled="!online || busy || running" @click="handlePull">更新镜像</n-button>
    <n-button size="small" type="error" secondary :disabled="!online || busy || running" @click="remove">删除</n-button>
  </div>

  <stack-pull-modal
    v-model:show="pullShow"
    :agent-id="agentId"
    :stack-name="stackName"
    :online="online"
    @finished="emit('changed')"
  />
</template>
<style scoped>
.stack-actions { display: flex; gap: 8px; flex-wrap: wrap; }
</style>
