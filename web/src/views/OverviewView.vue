<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NSelect, NSkeleton, NTag } from 'naive-ui'
import { listAgents } from '@/api/agents'
import { loadAgentOverview } from '@/api/overview'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import HostUsage from '@/components/overview/HostUsage.vue'
import StatCard from '@/components/overview/StatCard.vue'
import { usePolling } from '@/composables/usePolling'
import type { Agent } from '@/types/agent'
import { ApiError } from '@/types/api'
import type { AgentOverview } from '@/types/overview'
import { agentDisplayName } from '@/utils/agentDisplay'
import { getAgentHealth, HEALTH_STATES, sampleExpiresAt } from '@/utils/dashboardHealth'
import { formatAbsoluteTime } from '@/utils/formatTime'

const SELECT_KEY = 'arkade.overview.selectedAgent'
const router = useRouter()
const agents = shallowRef<Agent[] | null>(null)
const receivedAt = ref(0)
const now = ref(Date.now())
const refreshing = ref(false)
const errorText = ref('')
const selectedAgentId = ref(readSelection())
const details = shallowRef<AgentOverview | null>(null)
const detailLoading = ref(false)

function readSelection(): string | null {
  try { return sessionStorage.getItem(SELECT_KEY) || null }
  catch { return null }
}

function onSelectAgent(id: string | null): void {
  selectedAgentId.value = id
  try {
    if (id === null) sessionStorage.removeItem(SELECT_KEY)
    else sessionStorage.setItem(SELECT_KEY, id)
  } catch { /* Selection still works without sessionStorage. */ }
}

const rows = computed(() => (agents.value ?? [])
  .map((agent) => getAgentHealth(agent, now.value, receivedAt.value))
  .sort((a, b) => a.rank - b.rank || agentDisplayName(a.agent).localeCompare(agentDisplayName(b.agent), 'zh-CN') || a.agent.id.localeCompare(b.agent.id)))
const breakdown = computed(() => {
  const counts = new Map<string, number>()
  for (const { state } of rows.value) counts.set(state.key, (counts.get(state.key) ?? 0) + 1)
  return HEALTH_STATES.flatMap((state) => counts.has(state.key) ? [{ ...state, count: counts.get(state.key)! }] : [])
})
const agentOptions = computed(() => (agents.value ?? []).map((agent) => ({ label: agentDisplayName(agent), value: agent.id })))
const selectedAgent = computed(() => agents.value?.find((agent) => agent.id === selectedAgentId.value))
const selection = computed(() => details.value?.agentId === selectedAgentId.value ? details.value : null)

let listController: AbortController | undefined
let detailController: AbortController | undefined
let detailRequest: Promise<void> | undefined
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

function loadDetails(id: string): Promise<void> {
  if (detailController && detailRequest) return detailRequest
  const controller = new AbortController()
  detailController = controller
  detailLoading.value = true
  detailRequest = loadAgentOverview(id, controller.signal).then((result) => {
    if (!controller.signal.aborted && selectedAgentId.value === id) details.value = result
  }).finally(() => {
    if (detailController === controller) {
      detailController = undefined
      detailRequest = undefined
      detailLoading.value = false
    }
  })
  return detailRequest
}

function clearDetails(): void {
  detailController?.abort()
  detailController = undefined
  detailRequest = undefined
  detailLoading.value = false
  details.value = null
}

watch(selectedAgentId, (id) => {
  clearDetails()
  if (id && selectedAgent.value) void loadDetails(id)
}, { flush: 'sync' })

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
    const selected = selectedAgent.value
    if (selected) void loadDetails(selected.id)
    else if (selectedAgentId.value !== null) onSelectAgent(null)
  } catch (err) {
    if (controller.signal.aborted) return
    if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
      agents.value = null
      clearDetails()
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
  detailController?.abort()
  clearTimeout(expiryTimer)
})

function enterStacks(id: string) {
  return { name: 'agent-stacks', params: { id } }
}

function enterExternalStack(agentId: string, stackName: string) {
  return stackName ? { name: 'agent-stack-external-detail', params: { id: agentId, name: stackName } } : enterStacks(agentId)
}

const stackHint = computed(() => {
  const unmanaged = selection.value?.counts.unmanagedStacks
  if (unmanaged === undefined) return '未纳管数量未知'
  return unmanaged > 0 ? `另有 ${unmanaged} 个未纳管` : '无未纳管项目'
})
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
              <host-usage :label="row.sample.disk?.scope === 'host_filesystem' ? '磁盘（监控分区）' : '磁盘（根分区）'" :usage="row.sample.disk" />
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

      <section class="server-detail" aria-labelledby="detail-heading">
        <header class="server-detail__header">
          <div>
            <h2 id="detail-heading">单台服务器资源</h2>
            <p>选择服务器后，仅查询该台的编排和资源数量。</p>
          </div>
          <n-select :value="selectedAgentId" :options="agentOptions" clearable placeholder="选择服务器" aria-label="选择要统计的服务器" class="server-detail__select" @update:value="onSelectAgent" />
        </header>
        <n-skeleton v-if="detailLoading && !selection" height="92px" />
        <div v-if="selection" class="server-detail__result" :aria-busy="detailLoading">
          <n-alert v-if="selection.failures.length" type="warning" title="部分资源未能确认">
            <ul class="failure-list"><li v-for="failure in selection.failures" :key="failure.resource">{{ failure.resource }}：{{ failure.message }}</li></ul>
          </n-alert>
          <n-alert v-if="selection.driftedStacks.length" type="warning" title="编排配置发生漂移">
            服务器上的 Compose 配置与 Hub 记录不一致。保存前请先核对服务器上的真实配置。
            <div class="problem-links">
              <router-link v-for="stack in selection.driftedStacks" :key="stack" class="arkade-link" :to="enterStacks(selection.agentId)">{{ stack }}</router-link>
            </div>
          </n-alert>
          <n-alert v-if="selection.orphanStacks.length" type="info" title="存在未纳管的编排">
            可进入外部项目查看容器、日志与配置草稿。
            <div class="problem-links">
              <router-link v-for="stack in selection.orphanStacks" :key="stack" class="arkade-link" :to="enterExternalStack(selection.agentId, stack)">{{ stack }}</router-link>
            </div>
          </n-alert>
          <div class="stat-grid">
            <stat-card label="容器" :value="selection.counts.containers" unit="个" :hint="selection.counts.runningContainers === undefined ? '数量未知' : `运行中 ${selection.counts.runningContainers} 个`" />
            <stat-card label="已纳管编排" :value="selection.counts.stacks" unit="个" :hint="stackHint" />
            <stat-card label="本地镜像" :value="selection.counts.images" unit="个" />
            <stat-card label="网络" :value="selection.counts.networks" unit="个" />
            <stat-card label="存储卷" :value="selection.counts.volumes" unit="个" />
          </div>
        </div>
      </section>
    </template>
  </div>
</template>

<style scoped>
.dashboard { container-type: inline-size; }
.dashboard > .n-alert { margin-bottom: 16px; }
.fleet { border: 1px solid var(--arkade-border-color); border-radius: 10px; background: var(--arkade-surface-bg); }
.fleet__header, .server-detail__header { display: flex; align-items: center; justify-content: space-between; gap: 12px 20px; flex-wrap: wrap; }
.fleet__header { padding: 18px 20px 12px; }
.fleet__header h2, .server-detail__header h2 { font-size: 17px; line-height: 1.35; margin: 0; color: var(--arkade-text-primary); }
.fleet__header p, .server-detail__header p { margin: 5px 0 0; font-size: 12px; color: var(--arkade-text-secondary); }
.fleet__total { color: var(--arkade-text-secondary); white-space: nowrap; }
.fleet__total strong { font-size: 23px; font-variant-numeric: tabular-nums; color: var(--arkade-text-primary); }
.fleet__summary, .fleet__list { list-style: none; margin: 0; padding: 0; }
.fleet__summary { display: flex; gap: 6px; flex-wrap: wrap; padding: 0 20px 16px; }
.fleet__summary b { margin-inline-start: 4px; }
.fleet__row { display: grid; grid-template-columns: minmax(155px, 1.1fr) minmax(0, 3fr) 140px; align-items: start; gap: 12px 20px; border-top: 1px solid var(--arkade-border-color); border-inline-start: 3px solid transparent; padding: 15px 20px 15px 17px; }
.fleet__row:last-child { border-end-start-radius: 9px; border-end-end-radius: 9px; }
.fleet__row[data-state='offline'], .fleet__row[data-state='engine-error'] { border-inline-start-color: var(--arkade-danger); background: var(--arkade-surface-bg-subtle); }
.host-identity { display: flex; flex-direction: column; align-items: flex-start; gap: 5px; min-width: 0; }
.host-identity__name { font-weight: 600; overflow-wrap: anywhere; }
.host-metrics { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px 16px; }
.host-time, .host-no-data { color: var(--arkade-text-secondary); font-size: 12px; font-variant-numeric: tabular-nums; }
.host-time span, .host-no-data span { display: block; }
.server-detail { margin-top: 26px; }
.server-detail__header { margin-bottom: 12px; }
.server-detail__select { width: min(100%, 300px); }
.server-detail__result { display: grid; gap: 10px; }
.failure-list { margin: 0; padding-inline-start: 18px; overflow-wrap: anywhere; }
.problem-links { display: flex; flex-wrap: wrap; gap: 6px 14px; margin-top: 6px; overflow-wrap: anywhere; }
.stat-grid { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)); gap: 10px; }
@container (max-width: 820px) {
  .fleet__row { grid-template-columns: minmax(0, 1fr) auto; align-items: start; }
  .host-metrics, .host-no-data { grid-column: 1 / -1; grid-row: 2; }
  .host-time { text-align: end; }
  .stat-grid { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
@container (max-width: 440px) {
  .fleet__header { padding-inline: 12px; }
  .fleet__summary { padding-inline: 12px; }
  .fleet__row { grid-template-columns: minmax(0, 1fr); padding-inline: 9px 12px; }
  .host-time { text-align: start; }
  .host-time span { display: inline; margin-inline-end: 6px; }
  .host-metrics { gap: 8px; }
  .stat-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
</style>
