<script setup lang="ts">
/**
 * 容器详情页 /agents/:id/containers/:containerId
 *
 * ## 权限是两个层级，页面必须同时支持
 *
 * - **详情**（挂载、网络、资源限制）需要 `manage`——它暴露宿主路径等基础设施信息；
 * - **日志** 只需要 `view`。
 *
 * 所以「详情 403 但日志能看」是**正常组合**，不能因为详情失败就把整页变成错误页。
 * 这也是本页不用一个统一 loading/error 的原因。
 *
 * ## 契约里没有 Env
 *
 * 后端刻意不下发环境变量（常含密码/密钥）。前端也不会从 command 或
 * inspect 的任何字段反推——反推出来的东西既不可靠又可能泄露。
 */
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NSpin, NTabPane, NTabs } from 'naive-ui'
import PageHeader from '@/components/common/PageHeader.vue'
import ContainerDetailFacts from '@/components/container/ContainerDetailFacts.vue'
import ContainerMountsTable from '@/components/container/ContainerMountsTable.vue'
import ContainerDetailLogs from '@/components/container/ContainerDetailLogs.vue'
import { getContainer } from '@/api/containers'
import { useAgentsStore } from '@/stores/agents'
import { ApiError } from '@/types/api'
import type { ContainerDetail } from '@/types/container'

const route = useRoute()
const router = useRouter()
const agentsStore = useAgentsStore()

const agentId = computed<string>(() => String(route.params.id ?? ''))
const containerId = computed<string>(() => String(route.params.containerId ?? ''))

const agent = computed(() => agentsStore.findById(agentId.value))
const directAddress = computed<string>(() => agent.value?.direct_address ?? '')

const detail = ref<ContainerDetail | null>(null)
const loading = ref(false)
const errorText = ref('')
/** 详情权限不足：只影响概览/挂载，不影响日志 */
const forbidden = ref(false)

const activeTab = ref<'overview' | 'mounts' | 'logs'>('overview')

let seq = 0

async function load(): Promise<void> {
  const id = agentId.value
  const cid = containerId.value
  if (id === '' || cid === '') return
  const mine = ++seq
  loading.value = true
  errorText.value = ''
  forbidden.value = false
  try {
    const res = await getContainer(id, cid)
    if (mine !== seq) return
    detail.value = res
  } catch (err) {
    if (mine !== seq) return
    detail.value = null
    if (err instanceof ApiError && err.status === 403) {
      // 403 是权限，不是故障：页面其他部分（日志）仍然可用
      forbidden.value = true
    } else {
      errorText.value = err instanceof Error ? err.message : '容器详情加载失败'
    }
  } finally {
    if (mine === seq) loading.value = false
  }
}

const pageTitle = computed(() => {
  const d = detail.value
  if (d !== null && d.name !== '') return d.name
  return containerId.value !== '' ? containerId.value : '容器详情'
})

const subtitle = computed(() => {
  const d = detail.value
  if (d !== null && d.image !== '') return d.image
  return agent.value?.display_name ?? agentId.value
})

function back(): void {
  void router.push({ name: 'agent-containers', params: { id: agentId.value } })
}

onMounted(() => {
  void load()
})

/*
 * 路由切换时 Vue 会复用组件实例（只有 params 变），onMounted 不会再跑。
 * 不重新加载就会显示上一个容器的信息——那种错误比空白更危险，
 * 因为用户会照着错的信息去排查。
 */
watch(
  () => [agentId.value, containerId.value] as const,
  () => {
    detail.value = null
    errorText.value = ''
    forbidden.value = false
    activeTab.value = 'overview'
    void load()
  }
)
</script>

<template>
  <div class="container-detail">
    <page-header :title="pageTitle" :subtitle="subtitle">
      <template #actions>
        <n-button size="small" @click="back">返回容器列表</n-button>
        <n-button size="small" :loading="loading" @click="load">刷新</n-button>
      </template>
    </page-header>

    <n-alert v-if="errorText !== ''" type="error" :bordered="false">
      {{ errorText }}
    </n-alert>

    <!--
      403：详情需要管理权限，但日志不需要。
      说清楚「日志仍可查看」，否则用户会以为整页都用不了而直接离开。
    -->
    <n-alert v-if="forbidden" type="warning" :bordered="false" title="无权限查看容器详情">
      查看挂载、网络与资源限制需要该服务器的管理权限。日志不受影响，仍可在「日志」页查看。
    </n-alert>

    <n-spin v-if="loading && detail === null && !forbidden" size="small" />

    <n-tabs v-else v-model:value="activeTab" type="line" animated>
      <n-tab-pane name="overview" tab="概览">
        <container-detail-facts
          :detail="detail"
          :forbidden="forbidden"
          :direct-address="directAddress"
        />
      </n-tab-pane>

      <n-tab-pane name="mounts" tab="挂载">
        <container-mounts-table :mounts="detail?.mounts ?? []" :forbidden="forbidden" />
      </n-tab-pane>

      <!-- 日志：view 权限，独立于详情的 manage 权限 -->
      <n-tab-pane name="logs" tab="日志">
        <container-detail-logs :agent-id="agentId" :container-id="containerId" :name="pageTitle" />
      </n-tab-pane>
    </n-tabs>
  </div>
</template>

<style scoped>
.container-detail {
  display: flex;
  flex-direction: column;
  gap: 12px;
}
</style>
