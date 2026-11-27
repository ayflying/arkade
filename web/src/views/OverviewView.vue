<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, shallowRef } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NSkeleton, NTag } from 'naive-ui'
import { listAgents } from '@/api/agents'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import HostUsage from '@/components/overview/HostUsage.vue'
import HostRate from '@/components/overview/HostRate.vue'
import type { RateReading } from '@/components/overview/HostRate.vue'
import { usePolling } from '@/composables/usePolling'
import type { Agent } from '@/types/agent'
import { ApiError } from '@/types/api'
import { agentDisplayName } from '@/utils/agentDisplay'
import { getAgentHealth, HEALTH_STATES, sampleExpiresAt } from '@/utils/dashboardHealth'
import { formatAbsoluteTime } from '@/utils/formatTime'

const router = useRouter()
const agents = shallowRef<Agent[] | null>(null)
const receivedAt = ref(0)
const now = ref(Date.now())
const refreshing = ref(false)
const errorText = ref('')
const rows = computed(() => (agents.value ?? [])
  .map((agent) => getAgentHealth(agent, now.value, receivedAt.value))
  .sort((a, b) => a.rank - b.rank || agentDisplayName(a.agent).localeCompare(agentDisplayName(b.agent), 'zh-CN') || a.agent.id.localeCompare(b.agent.id)))
const breakdown = computed(() => {
  const counts = new Map<string, number>()
  for (const { state } of rows.value) counts.set(state.key, (counts.get(state.key) ?? 0) + 1)
  return HEALTH_STATES.flatMap((state) => counts.has(state.key) ? [{ ...state, count: counts.get(state.key)! }] : [])
})
let listController: AbortController | undefined
let expiryTimer: ReturnType<typeof setTimeout> | undefined

// One local expiry timer, no extra network requests and no per-row timers.
function updateClock(): void {
  clearTimeout(expiryTimer)
  now.value = Date.now()
  let next = Infinity
  for (const agent of agents.value ?? []) {
    if (agent.status !== 'online' || agent.disabled === true) continue
    const expires = sampleExpiresAt(agent.sampled_at, receivedAt.value)
    if (expires > now.value) next = Math.min(next, expires)
  }
  if (Number.isFinite(next)) expiryTimer = setTimeout(updateClock, next - now.value)
}

async function refresh(): Promise<void> {
  const controller = new AbortController()
  listController = controller
  refreshing.value = true
  try {
    const list = await listAgents({ signal: controller.signal, cache: 'no-store' })
    if (controller.signal.aborted) return
    receivedAt.value = Date.now()
    agents.value = list
    updateClock()
    errorText.value = ''
  } catch (err) {
    if (controller.signal.aborted) return
    if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
      agents.value = null
    }
    errorText.value = err instanceof ApiError ? err.message : '无法读取服务器列表'
    throw err
  } finally {
    if (!controller.signal.aborted) refreshing.value = false
  }
}

const { trigger } = usePolling(refresh, { name: 'dashboard', interval: 30_000, immediate: false })
onMounted(() => { void trigger() })
onScopeDispose(() => {
  listController?.abort()
  clearTimeout(expiryTimer)
})

function enterStacks(id: string) {
  return { name: 'agent-stacks', params: { id } }
}

/**
 * 网络读数：主行接收、次行发送。
 *
 * 没有采样时返回空对象，让 HostRate 显示「无数据」——不能兜底成 0 B/s，
 * 那会把「没采到」说成「没有流量」。
 */
function networkReadings(network?: { rx: number; tx: number }): { primary?: RateReading; secondary?: RateReading } {
  if (!network) return {}
  return {
    primary: { caption: '↓', bytesPerSec: network.rx },
    secondary: { caption: '↑', bytesPerSec: network.tx }
  }
}

/** 磁盘 IO 读数：主行读、次行写；语义与网络读数一致，缺采样不造假。 */
function diskIOReadings(io?: { read: number; write: number }): { primary?: RateReading; secondary?: RateReading } {
  if (!io) return {}
  return {
    primary: { caption: '读', bytesPerSec: io.read },
    secondary: { caption: '写', bytesPerSec: io.write }
  }
}
</script>

<template>
  <div class="dashboard">
    <page-header title="仪表盘" subtitle="查看有权访问的服务器、引擎状态和主机资源采样。">
      <template #actions>
        <n-button size="small" :loading="refreshing" @click="trigger()">刷新</n-button>
      </template>
    </page-header>

    <n-alert v-if="errorText" :type="agents ? 'warning' : 'error'" title="未能刷新服务器健康" role="alert">
      {{ errorText }}。{{ agents ? '服务器状态为上次查询结果，不代表当前状态。' : '请点击刷新重试。' }}
    </n-alert>
    <n-skeleton v-if="agents === null && !errorText" height="156px" />
    <empty-state v-else-if="agents?.length === 0" description="暂无可访问的服务器。可在服务器页查看接入方式，或联系管理员确认授权。">
      <template #extra><n-button size="small" @click="router.push({ name: 'agents' })">查看服务器</n-button></template>
    </empty-state>

    <template v-if="agents?.length">
      <section class="fleet" aria-labelledby="fleet-heading">
        <header class="fleet__header">
          <div>
            <h2 id="fleet-heading">服务器健康</h2>
            <p>{{ errorText ? '上次成功查询' : '列表更新' }}：{{ formatAbsoluteTime(receivedAt / 1000) }}</p>
          </div>
          <span class="fleet__total"><strong>{{ agents.length }}</strong> 台可访问</span>
        </header>
        <ul class="fleet__summary" aria-label="各健康状态的服务器数量">
          <li v-for="state in breakdown" :key="state.key">
            <n-tag :type="state.tone" :bordered="false" size="small" round>
              {{ state.label }} <b>{{ state.count }}</b>
            </n-tag>
          </li>
        </ul>
        <ul class="fleet__list" aria-label="服务器健康列表，离线与异常优先">
          <li v-for="row in rows" :key="row.agent.id" class="fleet__row" :data-state="row.state.key">
            <div class="host-identity">
              <router-link class="arkade-link host-identity__name" :to="enterStacks(row.agent.id)">{{ agentDisplayName(row.agent) }}</router-link>
              <n-tag :type="row.state.tone" size="small" :bordered="false">{{ row.state.label }}</n-tag>
            </div>
            <div v-if="row.sample && !errorText" class="host-metrics">
              <host-usage label="CPU" :cpu-percent="row.sample.cpuPercent" />
              <host-usage label="内存" :usage="row.sample.memory" />
              <host-rate label="网络" v-bind="networkReadings(row.sample.network)" />
              <host-rate label="磁盘 IO" v-bind="diskIOReadings(row.sample.diskIO)" />
              <host-usage v-if="row.sample.disk" :label="row.sample.disk.scope === 'host_filesystem' ? '磁盘（监控分区）' : '磁盘（根分区）'" :usage="row.sample.disk" />
            </div>
            <div v-else class="host-no-data">
              {{ errorText ? '当前用量未确认' : row.agent.status === 'online' && !row.agent.disabled ? '暂无有效用量采样' : '当前无可用用量' }}
              <span v-if="row.state.key === 'no-data' && !errorText">{{ row.runtimeOk === true ? '引擎可达' : '引擎状态未知' }}</span>
            </div>
            <div class="host-time">
              <template v-if="row.sample && !errorText"><span>采样时间</span>{{ formatAbsoluteTime(row.agent.sampled_at) }}</template>
              <template v-else-if="row.agent.last_seen > 0"><span>最后心跳</span>{{ formatAbsoluteTime(row.agent.last_seen) }}</template>
              <span v-else>无心跳记录</span>
            </div>
          </li>
        </ul>
      </section>
    </template>
  </div>
</template>

<style scoped>
.dashboard { container-type: inline-size; }
.dashboard > .n-alert { margin-bottom: 16px; }
.fleet { border: 1px solid var(--arkade-border-color); border-radius: 10px; background: var(--arkade-surface-bg); }
.fleet__header { display: flex; align-items: center; justify-content: space-between; gap: 12px 20px; flex-wrap: wrap; }
.fleet__header { padding: 18px 20px 12px; }
.fleet__header h2 { font-size: 17px; line-height: 1.35; margin: 0; color: var(--arkade-text-primary); }
.fleet__header p { margin: 5px 0 0; font-size: 12px; color: var(--arkade-text-secondary); }
.fleet__total { color: var(--arkade-text-secondary); white-space: nowrap; }
.fleet__total strong { font-size: 23px; font-variant-numeric: tabular-nums; color: var(--arkade-text-primary); }
.fleet__summary, .fleet__list { list-style: none; margin: 0; padding: 0; }
.fleet__summary { display: flex; gap: 6px; flex-wrap: wrap; padding: 0 20px 16px; }
.fleet__summary b { margin-inline-start: 4px; }
.fleet__row { display: grid; grid-template-columns: minmax(145px, 1fr) minmax(0, 3.4fr) 132px; align-items: start; gap: 12px 20px; border-top: 1px solid var(--arkade-border-color); border-inline-start: 3px solid transparent; padding: 15px 20px 15px 17px; }
.fleet__row:last-child { border-end-start-radius: 9px; border-end-end-radius: 9px; }
.fleet__row[data-state='offline'], .fleet__row[data-state='engine-error'] { border-inline-start-color: var(--arkade-danger); background: var(--arkade-surface-bg-subtle); }
.host-identity { display: flex; flex-direction: column; align-items: flex-start; gap: 5px; min-width: 0; }
.host-identity__name { font-weight: 600; overflow-wrap: anywhere; }
.host-metrics { display: grid; grid-template-columns: repeat(auto-fit, minmax(132px, 1fr)); gap: 8px 16px; }
.host-time, .host-no-data { color: var(--arkade-text-secondary); font-size: 12px; font-variant-numeric: tabular-nums; }
.host-time span, .host-no-data span { display: block; }
@container (max-width: 820px) {
  .fleet__row { grid-template-columns: minmax(0, 1fr) auto; align-items: start; }
  .host-metrics, .host-no-data { grid-column: 1 / -1; grid-row: 2; }
  .host-time { text-align: end; }
}
@container (max-width: 440px) {
  .fleet__header { padding-inline: 12px; }
  .fleet__summary { padding-inline: 12px; }
  .fleet__row { grid-template-columns: minmax(0, 1fr); padding-inline: 9px 12px; }
  .host-time { text-align: start; }
  .host-time span { display: inline; margin-inline-end: 6px; }
  .host-metrics { gap: 8px; grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
