import { onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useExternalStack } from '@/composables/useExternalStack'
import { listAgents } from '@/api/agents'
import { applyExternalStack, copyExternalConfig, previewExternalApply } from '@/api/externalStacks'
import { ApiError } from '@/types/api'
import type { Agent } from '@/types/agent'
import type { ApplyPreviewResponse } from '@/types/externalStack'
import { confirmAction } from '@/utils/confirm'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'

export function useExternalStackConfigActions(props: { agentId: string; stackName: string }, changed: () => void) {
  const router = useRouter()
  let alive = true
  onUnmounted(() => { alive = false })
  const agentId = { value: props.agentId }
  const stackName = { value: props.stackName }

  const store = useExternalStack({
    agentId: () => agentId.value ?? '',
    name: () => stackName.value,
    configOnly: true
  })

  const applying = ref(false)
  const preview = ref<ApplyPreviewResponse | null>(null)
  const applyError = ref('')
  const alreadyManaged = ref(false)

  const copyShow = ref(false)
  const copying = ref(false)
  const copyError = ref('')
  const agentList = ref<Agent[]>([])

  async function handleSaveDraft(content: string, expectedVersion: number): Promise<void> {
    const ok = await store.saveDraft(content, expectedVersion)
    if (!alive) return
    if (ok) {
      toastSuccess('草稿已保存到 Hub')
    } else {
      toastError(store.draftError.value === '' ? '草稿保存失败' : store.draftError.value)
    }
  }

  async function handleApply(content: string): Promise<void> {
    const targetAgentId = agentId.value
    const targetName = stackName.value
    if (targetAgentId === null || targetName === '' || applying.value) return
    applying.value = true
    applyError.value = ''
    preview.value = null
    alreadyManaged.value = false
    try {
      const pre = await previewExternalApply(targetAgentId, targetName, { content })
      if (!alive) return
      preview.value = pre
      if (!pre.can_import) {
        // 不弹 toast，就地展示 blockers 更有用
        return
      }
      if (!(await confirmAction({ title: '确认登记到 Arkade？', content: `将「${targetName}」的当前编辑内容登记到 Hub，不启动、不重启容器。`, positiveText: '确认登记' })) || !alive) return
      const res = await applyExternalStack(targetAgentId, targetName, {
        content,
        expected_content_hash: pre.config_hash,
        confirmation: true
      })
      if (!alive) return
      if (res.sync_ok) {
        toastSuccess('已登记到 Arkade')
      } else {
        toastWarning(res.sync_hint ?? '已登记到 Hub，但尚未同步到服务器。')
      }
      changed()
    } catch (err) {
      if (!alive) return
      if (err instanceof ApiError && err.status === 409) {
        applyError.value = err.message === '' ? '登记冲突，请查看已纳管项目确认。' : err.message
        alreadyManaged.value = true
      } else {
        applyError.value = err instanceof Error ? err.message : '登记失败'
      }
    } finally {
      if (alive) applying.value = false
    }
  }

  function goManagedDetail(): void {
    if (agentId.value === null) return
    void router.push({
      name: 'agent-stack-detail',
      params: { id: agentId.value, name: stackName.value }
    })
  }

  const editorContent = ref('')

  async function openCopy(): Promise<void> {
    copyError.value = ''
    try {
      const agents = await listAgents()
      if (!alive) return
      agentList.value = agents
    } catch (err) {
      if (!alive) return
      copyError.value = err instanceof Error ? err.message : '服务器列表加载失败'
    }
    if (alive) copyShow.value = true
  }

  async function handleCopy(payload: { targetAgentId: string; targetName: string }): Promise<void> {
    const sourceAgentId = agentId.value
    const sourceName = stackName.value
    const content = editorContent.value
    if (sourceAgentId === null || sourceName === '' || copying.value) return
    copying.value = true
    copyError.value = ''
    try {
      await copyExternalConfig(sourceAgentId, sourceName, {
        target_agent_id: payload.targetAgentId,
        target_name: payload.targetName,
        content,
        confirmation: true
      })
      if (!alive) return
      copyShow.value = false
      // 只能说「目标 Hub 记录」，不能说「已到目标服务器」——配置还没下发
      toastSuccess('已复制到目标 Hub 记录，尚未下发到目标服务器，也不会自动启动')
    } catch (err) {
      if (!alive) return
      if (err instanceof ApiError && err.status === 403) {
        copyError.value = '复制配置需要对源与目标服务器都有管理权限。'
      } else if (err instanceof ApiError && err.status === 409) {
        copyError.value = `目标服务器上已存在同名编排「${payload.targetName}」，请换一个名称或先在目标上处理该项目。`
      } else {
        copyError.value = err instanceof Error ? err.message : '复制失败'
      }
    } finally {
      if (alive) copying.value = false
    }
  }

  onMounted(() => { void store.loadConfig() })

  return { store, applying, preview, applyError, alreadyManaged, copyShow, copying, copyError, agentList, editorContent, agentId, stackName, handleSaveDraft, handleApply, goManagedDetail, openCopy, handleCopy }
}
