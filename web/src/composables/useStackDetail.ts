import { onScopeDispose, ref, shallowRef, watch } from 'vue'
import { getUnifiedStack } from '@/api/stacks'
import type { UnifiedStack } from '@/types/stack'
import type { Container } from '@/types/container'
import { sameStackTarget } from '@/components/stack/stackDetailModel'

export function useStackDetail(target: () => readonly [string, string]) {
  const stack = shallowRef<UnifiedStack | null>(null)
  const containers = shallowRef<Container[]>([])
  const loading = ref(false), error = ref(''), containersError = ref('')
  let seq = 0
  let refreshTimer: ReturnType<typeof setTimeout> | undefined

  function stopRefresh() {
    clearTimeout(refreshTimer)
    refreshTimer = undefined
  }

  async function fetchDetail(background: boolean) {
    stopRefresh()
    const snapshot = target(), mine = ++seq
    loading.value = !background && Boolean(snapshot[0] && snapshot[1])
    if (!snapshot[0] || !snapshot[1]) return
    if (!background) error.value = ''
    try {
      const res = await getUnifiedStack(...snapshot)
      if (mine !== seq || !sameStackTarget(snapshot, target())) return
      stack.value = res.stack
      containers.value = res.containers
      containersError.value = res.containers_error ?? ''
      error.value = ''
    } catch (err) {
      if (mine !== seq || !sameStackTarget(snapshot, target())) return
      error.value = err instanceof Error ? err.message : '加载编排详情失败'
    } finally {
      if (mine === seq && sameStackTarget(snapshot, target())) {
        loading.value = false
        // 页面可能在任务进行中打开，此时没有操作弹窗通知它何时刷新。
        // 上次读取结束后才安排下一次，避免请求重叠；忙碌状态解除后停止。
        if (stack.value?.status === 'busy') {
          refreshTimer = setTimeout(() => { void fetchDetail(true) }, 5000)
        }
      }
    }
  }

  const load = () => fetchDetail(false)
  watch(target, () => {
    stack.value = null
    containers.value = []
    containersError.value = ''
    error.value = ''
    void load()
  }, { immediate: true })
  onScopeDispose(() => { seq++; stopRefresh() })
  return { stack, containers, containersError, loading, error, load }
}
