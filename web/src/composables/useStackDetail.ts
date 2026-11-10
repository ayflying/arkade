import { onUnmounted, ref, shallowRef, watch } from 'vue'
import { getUnifiedStack } from '@/api/stacks'
import type { UnifiedStack } from '@/types/stack'
import type { Container } from '@/types/container'
import { sameStackTarget } from '@/components/stack/stackDetailModel'

export function useStackDetail(target: () => readonly [string, string]) {
  const stack = shallowRef<UnifiedStack | null>(null)
  const containers = shallowRef<Container[]>([])
  const loading = ref(false), error = ref(''), containersError = ref('')
  let seq = 0
  async function load() {
    const snapshot = target(), mine = ++seq
    if (!snapshot[0] || !snapshot[1]) return
    loading.value = true; error.value = ''
    try {
      const res = await getUnifiedStack(...snapshot)
      if (mine !== seq || !sameStackTarget(snapshot, target())) return
      stack.value = res.stack; containers.value = res.containers; containersError.value = res.containers_error ?? ''
    } catch (err) {
      if (mine !== seq || !sameStackTarget(snapshot, target())) return
      error.value = err instanceof Error ? err.message : '加载编排详情失败'
    } finally { if (mine === seq) loading.value = false }
  }
  watch(target, () => { seq++; stack.value = null; containers.value = []; error.value = ''; void load() }, { immediate: true })
  onUnmounted(() => { seq++; stack.value = null; containers.value = [] })
  return { stack, containers, containersError, loading, error, load }
}
