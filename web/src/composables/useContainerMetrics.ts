import { computed, onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import { getContainerMetrics } from '@/api/containerMetrics'
import { usePolling } from '@/composables/usePolling'
import { ApiError } from '@/types/api'
import type { ContainerMetricsSnapshot, ContainerMetricsStatus } from '@/types/containerMetrics'
import { indexContainerMetrics, isContainerSampleStale, sampleExpiresAt } from '@/utils/containerMetrics'

export function useContainerMetrics(
  agentId: Readonly<Ref<string>>,
  enabled: Readonly<Ref<boolean>>
) {
  const snapshot = shallowRef<ContainerMetricsSnapshot | null>(null)
  const status = ref<ContainerMetricsStatus>('waiting')
  const now = ref(Date.now())
  const byId = computed(() => indexContainerMetrics(snapshot.value?.containers ?? []))
  const byStaleId = computed(() => {
    const stale = new Set<string>()
    for (const [id, sample] of byId.value) {
      const expiresAt = sampleExpiresAt(sample, snapshot.value?.stale_after_seconds ?? 0)
      if (isContainerSampleStale(sample, expiresAt, now.value)) stale.add(id)
    }
    return stale
  })

  let scope = 0
  let pending: Promise<void> | null = null

  function load(): Promise<void> {
    const id = agentId.value
    if (id === '' || !enabled.value) return Promise.resolve()
    if (pending !== null) return pending

    const mine = scope
    pending = getContainerMetrics(id).then((result) => {
      if (mine !== scope) return
      if (result?.agent_id !== id || !Array.isArray(result.containers)) {
        throw new Error('无效的容器指标快照')
      }
      now.value = Date.now()
      snapshot.value = result
      status.value = 'waiting'
    }).catch((err: unknown) => {
      if (mine !== scope) return
      if (err instanceof ApiError && (err.status === 403 || err.status === 404)) {
        snapshot.value = null
        status.value = err.status === 403 ? 'forbidden' : 'unsupported'
        return
      }
      // Keep the last successful sample; its timestamp, not request success, controls freshness.
      status.value = 'error'
      throw err
    }).finally(() => {
      if (mine === scope) pending = null
    })
    return pending
  }

  function refresh(): Promise<void> {
    return load().catch(() => { /* Automatic refresh uses usePolling's backoff. */ })
  }

  watch(agentId, () => {
    ++scope
    pending = null
    snapshot.value = null
    status.value = 'waiting'
  }, { flush: 'sync' })

  watch([agentId, enabled], ([, active]) => {
    if (active) void refresh()
  }, { immediate: true })

  const canPoll = computed(() => enabled.value && agentId.value !== '' &&
    status.value !== 'unsupported' && status.value !== 'forbidden')

  // Metrics are cached by the Hub; no additional Docker inspect or per-row request is needed.
  usePolling(load, { name: 'container-metrics', interval: 5000, enabled: canPoll, immediate: false })

  watch([snapshot, now], ([current, timestamp], _, onCleanup) => {
    if (current === null) return
    let next = Number.POSITIVE_INFINITY
    for (const sample of current.containers) {
      if (sample.stale) continue
      const deadline = sampleExpiresAt(sample, current.stale_after_seconds)
      if (deadline > timestamp && deadline < next) next = deadline
    }
    if (!Number.isFinite(next)) return
    // A local display deadline (no HTTP). Even with auto-refresh disabled, old values age out.
    const timer = setTimeout(() => { now.value = Date.now() }, Math.max(0, next - Date.now()))
    onCleanup(() => clearTimeout(timer))
  })

  onScopeDispose(() => { ++scope })

  return { byId, byStaleId, emptyStatus: status, refresh }
}
