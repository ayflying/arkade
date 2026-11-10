import { computed, onUnmounted, ref } from 'vue'
import { getStackConfig, updateStack } from '@/api/stacks'
import { ApiError } from '@/types/api'
import { confirmAction } from '@/utils/confirm'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'
import type { UpdateStackRequest } from '@/types/stack'

/** Omission preserves .env; an explicit empty string clears it. */
export function managedUpdatePayload(content: string, version: number, env: string, originalEnv: string): UpdateStackRequest {
  return { content, version, ...(env !== originalEnv ? { env_content: env } : {}) }
}

export function useManagedStackConfig(agentId: string, name: string, changed: () => void) {
  const content = ref(''), originalContent = ref(''), envContent = ref(''), originalEnv = ref('')
  const version = ref(0), loaded = ref(false), loading = ref(false), saving = ref(false)
  const error = ref(''), saveError = ref(''), syncWarning = ref('')
  let seq = 0, alive = true
  const dirty = computed(() => content.value !== originalContent.value || envContent.value !== originalEnv.value)
  async function load() {
    const mine = ++seq
    loading.value = true
    loaded.value = false
    error.value = ''
    try {
      const res = await getStackConfig(agentId, name)
      if (!alive || mine !== seq) return
      content.value = originalContent.value = res.content
      envContent.value = originalEnv.value = res.env_content
      version.value = res.version
      loaded.value = true
    } catch (err) {
      if (!alive || mine !== seq) return
      error.value = err instanceof ApiError && err.status === 403
        ? '查看或编辑配置需要该服务器的管理权限。容器与日志仍可查看。'
        : err instanceof Error ? err.message : '加载配置失败'
    } finally { if (alive && mine === seq) loading.value = false }
  }
  async function save() {
    if (!loaded.value || saving.value || !dirty.value) return
    saving.value = true
    // Snapshot the exact content being confirmed; later editing cannot change this request.
    const payload = managedUpdatePayload(content.value, version.value, envContent.value, originalEnv.value)
    const submittedEnv = envContent.value
    try {
      if (!(await confirmAction({ title: '保存并部署配置？', content: `将保存「${name}」的配置并立即执行 compose up，可能创建或重建容器。这不是草稿保存。`, positiveText: '保存并部署', danger: true })) || !alive) return
      saveError.value = ''
      const res = await updateStack(agentId, name, payload)
      if (!alive) return
      version.value = res.stack.version
      originalContent.value = payload.content
      originalEnv.value = submittedEnv
      syncWarning.value = res.sync_ok ? '' : res.sync_hint ?? '配置已存入 Hub，但尚未同步到服务器；请检查连接后手动重新下发。'
      if (res.sync_ok) toastSuccess('配置已保存，部署操作已执行')
      else toastWarning(syncWarning.value)
      changed()
    } catch (err) {
      if (!alive) return
      saveError.value = err instanceof ApiError && (err.status === 409 || err.code === 'conflict')
        ? '内容已被他人修改。请先复制当前修改，再重新加载配置。'
        : err instanceof Error ? err.message : '保存并部署失败'
      toastError(saveError.value)
    } finally { if (alive) saving.value = false }
  }
  function reset() { content.value = originalContent.value; envContent.value = originalEnv.value; saveError.value = '' }
  onUnmounted(() => {
    alive = false; seq++
    content.value = originalContent.value = envContent.value = originalEnv.value = ''
  })
  return { content, envContent, version, loaded, loading, saving, error, saveError, syncWarning, dirty, load, save, reset }
}
