<script setup lang="ts">
/**
 * 容器详情页 /agents/:id/containers/:containerId
 *
 * ## 权限是两个层级，页面必须同时支持
 *
 * - **详情**（挂载、网络、资源限制）需要 `manage`——它暴露宿主路径等基础设施信息；
 * - **容器指标与日志** 只需要 `view`。
 *
 * 所以「详情 403 但日志能看」是**正常组合**，不能因为详情失败就把整页变成错误页。
 * 这也是本页不用一个统一 loading/error 的原因。
 *
 * ## 容器操作集中在这里
 *
 * 列表页去掉了行内按钮，改为「勾选后批量」。单个容器的启停/重启/删除/重建
 * 都放在本页：一来操作的对象就在眼前（名称、镜像、状态一目了然），
 * 二来重建这种会换掉容器 ID 的动作需要更多解释空间，行内按钮装不下。
 *
 * ## 契约里没有 Env
 *
 * 后端刻意不下发环境变量（常含密码/密钥）。前端也不会从 command 或
 * inspect 的任何字段反推——反推出来的东西既不可靠又可能泄露。
 */
import { computed, onMounted, ref, shallowRef, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NPopconfirm, NSpace, NSpin, NTabPane, NTabs, NTooltip } from 'naive-ui'
import PageHeader from '@/components/common/PageHeader.vue'
import ContainerDetailFacts from '@/components/container/ContainerDetailFacts.vue'
import ContainerDetailMetrics from '@/components/container/ContainerDetailMetrics.vue'
import ContainerMetricHistory from '@/components/container/ContainerMetricHistory.vue'
import ContainerMountsTable from '@/components/container/ContainerMountsTable.vue'
import ContainerDetailLogs from '@/components/container/ContainerDetailLogs.vue'
import ContainerInspectPanel from '@/components/container/ContainerInspectPanel.vue'
import ContainerConsole from '@/components/container/ContainerConsole.vue'
import {
  deleteContainer,
  getContainer,
  recreateContainer,
  restartContainer,
  startContainer,
  stopContainer
} from '@/api/containers'
import { useContainerMetrics } from '@/composables/useContainerMetrics'
import { useAgentsStore } from '@/stores/agents'
import { ApiError } from '@/types/api'
import type { ContainerDetail } from '@/types/container'
import {
  canRestartContainer,
  canStartContainer,
  canStopContainer,
  containerActionBlockReason,
  isAgentOnline,
  needsForceDelete
} from '@/utils/agentAction'
import { confirmWithOption } from '@/utils/confirm'
import { toastError, toastSuccess } from '@/utils/feedback'

const route = useRoute()
const router = useRouter()
const agentsStore = useAgentsStore()

const agentId = computed<string>(() => String(route.params.id ?? ''))
const containerId = computed<string>(() => String(route.params.containerId ?? ''))

const agent = computed(() => agentsStore.findById(agentId.value))
const directAddress = computed<string>(() => agent.value?.direct_address ?? '')

const detail = shallowRef<ContainerDetail | null>(null)
const loading = ref(false)
const errorText = ref('')
/** 详情权限不足：只影响概览/挂载，不影响日志 */
const forbidden = ref(false)
/** 有容器操作正在执行（批量按钮与单个操作共用，避免并发下发） */
const acting = ref(false)

const activeTab = ref<'overview' | 'inspect' | 'metrics' | 'mounts' | 'logs' | 'console'>('overview')
const metricsEnabled = computed(() => activeTab.value === 'overview' && containerId.value !== '')
const metrics = useContainerMetrics(agentId, metricsEnabled)
/** 控制台只在服务器在线时可执行（离线请求会 503） */
const agentOnline = computed(() => isAgentOnline(agent.value))
// Links from container tables use full IDs. For legacy short links, only the
// inspect response can safely resolve the canonical ID; the cache may omit rows.
const metricContainerId = computed(() => detail.value?.id || containerId.value)
const sample = computed(() => metrics.byId.value.get(metricContainerId.value))
const sampleStale = computed(() => metrics.byStaleId.value.has(metricContainerId.value))
const emptyMetricsStatus = metrics.emptyStatus

async function refresh(): Promise<void> {
  await Promise.all([load(), metrics.refresh()])
}

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

/* ───────────────────────── 操作可用性 ───────────────────────── */

/** 当前状态：优先用详情里的真实状态，未加载时退回 unknown（所有操作都不可用） */
const currentState = computed<string>(() => detail.value?.state ?? 'unknown')

/**
 * 禁用原因。
 *
 * 用详情里的状态判定，而不是列表里的（本页可能直接打开一个链接）。
 * 返回 null 表示可操作。
 */
const blockReason = computed<string | null>(() => {
  if (detail.value === null) return '容器信息尚未加载'
  return containerActionBlockReason(agent.value, { state: currentState.value })
})

const canStart = computed(() => blockReason.value === null && canStartContainer(currentState.value))
const canStop = computed(() => blockReason.value === null && canStopContainer(currentState.value))
const canRestart = computed(
  () => blockReason.value === null && canRestartContainer(currentState.value)
)
/** 删除与重建：只要求服务器在线且详情已加载 */
const canManage = computed(() => blockReason.value === null)
/** 操作按钮的总禁用开关 */
const disabled = computed(() => acting.value || !canManage.value || forbidden.value)

/* ───────────────────────── 单个操作 ───────────────────────── */

/** 统一处理"调用成功/失败 → 提示 → 刷新详情" */
async function runOp(
  action: () => Promise<{ state: string }>,
  successText: string
): Promise<boolean> {
  acting.value = true
  try {
    await action()
    toastSuccess(successText)
    await refresh()
    return true
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : '操作失败，请稍后重试')
    return false
  } finally {
    acting.value = false
  }
}

async function handleStart(): Promise<void> {
  if (!canStart.value) return
  await runOp(
    () => startContainer(agentId.value, containerId.value),
    `容器「${pageTitle.value}」已启动`
  )
}

async function handleStop(): Promise<void> {
  if (!canStop.value) return
  await runOp(
    () => stopContainer(agentId.value, containerId.value),
    `容器「${pageTitle.value}」已停止`
  )
}

/**
 * 重启：不弹确认。
 *
 * 与列表页原来的行为保持一致——重启是恢复性且可重复的动作，
 * 误点代价只是短暂中断；每次都弹窗会让最常见的排障动作变得很烦。
 */
async function handleRestart(): Promise<void> {
  if (!canRestart.value) return
  await runOp(
    () => restartContainer(agentId.value, containerId.value),
    `容器「${pageTitle.value}」已重启`
  )
}

/**
 * 删除容器。
 *
 * 三个必须讲清的点：
 * 1. **不可恢复**：删掉就没了，不能像停止那样再启动回来；
 * 2. **运行中的容器要强制停止**：不说明的话用户会奇怪「为什么还能删正在跑的」；
 * 3. **匿名数据卷默认保留**：删卷是数据丢失，必须主动勾选。
 */
async function handleDelete(): Promise<void> {
  if (!canManage.value) return
  const running = needsForceDelete(currentState.value)
  const result = await confirmWithOption({
    title: `确认删除容器「${pageTitle.value}」？`,
    content: running
      ? '该容器正在运行，将强制停止并删除。'
      : '该容器当前未在运行。',
    notes: ['删除后容器无法恢复，容器内的非持久化数据会一并消失。'],
    optionLabel: '同时删除匿名数据卷（数据将永久丢失）',
    optionDefault: false,
    optionHint: '⚠️ 容器使用的匿名数据卷将一并删除，卷内数据永久丢失且无法恢复。',
    positiveText: '删除',
    negativeText: '取消',
    danger: true
  })
  if (!result.confirmed) return

  acting.value = true
  try {
    await deleteContainer(agentId.value, containerId.value, {
      force: running,
      removeVolumes: result.optionChecked
    })
    toastSuccess(`容器「${pageTitle.value}」已删除`)
    // 容器已经不存在：留在详情页只会显示一个 404，回到列表更合理
    back()
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : '删除容器失败')
  } finally {
    acting.value = false
  }
}

/**
 * 重建容器：按原配置删旧建新，可勾选先拉取镜像。
 *
 * 这个确认框对应 Portainer 的 "Recreate + Pull latest image"，
 * 也是本页唯一会**换掉容器 ID** 的操作，所以说明必须比其它操作更详细：
 * 什么会保留、什么会丢、属于编排时会发生什么。
 */
async function handleRecreate(): Promise<void> {
  if (!canManage.value) return
  const result = await confirmWithOption({
    title: `重建容器「${pageTitle.value}」？`,
    content: '将按该容器当前的配置删除旧容器，并用相同配置创建同名新容器。',
    notes: [
      '重建会换掉容器对象：容器 ID 变化、日志从头开始、容器内未挂载到卷的数据会丢失。',
      '镜像、环境变量、端口映射、挂载、网络、重启策略与资源限制都会沿用原配置，数据卷与绑定目录中的数据保留。已停止的容器重建后仍是停止状态，不会被自动启动。',
      '若该容器是由编排（docker compose）创建的，单独重建不会修改编排文件，下次执行编排部署可能被编排里的镜像覆盖。'
    ],
    optionLabel: '重新拉取镜像（拉取该镜像的最新版本后再重建）',
    optionDefault: false,
    optionHint: '将先从镜像仓库拉取最新版本。拉取可能耗时数分钟，且拉到的版本可能与该容器当前运行版本不同。',
    positiveText: '重建',
    negativeText: '取消',
    danger: true
  })
  if (!result.confirmed) return

  acting.value = true
  try {
    const res = await recreateContainer(agentId.value, containerId.value, {
      pull: result.optionChecked
    })
    toastSuccess(`容器「${pageTitle.value}」已重建`)
    /*
     * 重建返回的是新容器 ID（可能是短 ID，取决于 Agent 能否回读列表）。
     * 必须把路由切到新 ID：否则页面还挂着已删除的旧 ID，
     * 下一次操作会报"容器不存在"。短 ID 能被 inspect 解析，页面会自行回填完整 ID。
     */
    const nextId = typeof res.container_id === 'string' ? res.container_id.trim() : ''
    if (nextId !== '' && nextId !== containerId.value) {
      await router.replace({
        name: 'agent-container-detail',
        params: { id: agentId.value, containerId: nextId }
      })
    } else {
      await refresh()
    }
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : '重建容器失败')
  } finally {
    acting.value = false
  }
}

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
        <n-button size="small" :loading="loading" @click="refresh">刷新</n-button>
      </template>
    </page-header>

    <!--
      操作区：容器的所有单实例操作都在这里。
      按钮顺序按"破坏性递增"排：启停 → 重启 → 重建 → 删除，
      删除放最右且用红色，避免和日常操作混在一起。
    -->
    <div class="container-detail__actions">
      <n-space align="center" :size="8">
        <n-tooltip :disabled="canStart || blockReason === null" trigger="hover">
          <template #trigger>
            <n-button size="small" :disabled="disabled || !canStart" :loading="acting" @click="handleStart">
              启动
            </n-button>
          </template>
          {{ blockReason ?? '容器已在运行' }}
        </n-tooltip>

        <n-tooltip :disabled="canStop || blockReason === null" trigger="hover">
          <template #trigger>
            <n-button size="small" :disabled="disabled || !canStop" :loading="acting" @click="handleStop">
              停止
            </n-button>
          </template>
          {{ blockReason ?? '容器未在运行' }}
        </n-tooltip>

        <n-button size="small" :disabled="disabled || !canRestart" :loading="acting" @click="handleRestart">
          重启
        </n-button>

        <n-button
          size="small"
          secondary
          :disabled="disabled"
          :loading="acting"
          @click="handleRecreate"
        >
          重建
        </n-button>

        <!--
          删除再套一层气泡确认：这是本页唯一不可撤销的操作，
          而且它紧挨着"重建"，误点代价极高。
        -->
        <n-popconfirm
          :disabled="disabled"
          positive-text="继续"
          negative-text="取消"
          @positive-click="handleDelete"
        >
          <template #trigger>
            <n-button size="small" type="error" secondary :disabled="disabled" :loading="acting">
              删除
            </n-button>
          </template>
          删除容器不可恢复，确认继续？
        </n-popconfirm>

        <n-button
          v-if="activeTab !== 'logs'"
          size="small"
          quaternary
          @click="activeTab = 'logs'"
        >
          查看日志
        </n-button>
      </n-space>
      <!-- 不可操作时把原因直接写在按钮旁边，而不是只放在 tooltip 里 -->
      <span v-if="blockReason !== null" class="container-detail__block-reason">
        {{ blockReason }}
      </span>
    </div>

    <n-alert v-if="errorText !== ''" type="error" :bordered="false">
      {{ errorText }}
    </n-alert>

    <!--
      403：详情需要管理权限，但日志不需要。
      说清楚「日志仍可查看」，否则用户会以为整页都用不了而直接离开。
    -->
    <n-alert v-if="forbidden" type="warning" :bordered="false" title="无权限查看容器详情">
      查看挂载、网络配置与资源限制、以及对容器执行操作都需要该服务器的管理权限。日志不受影响，仍可在「日志」页查看；下方仍可查看容器指标。
    </n-alert>

    <n-spin v-if="loading && detail === null && !forbidden" size="small" />

    <n-tabs v-else v-model:value="activeTab" type="line" animated>
      <n-tab-pane name="overview" tab="概览">
        <container-detail-metrics
          class="container-detail__metrics"
          :sample="sample"
          :stale="sampleStale"
          :empty-status="emptyMetricsStatus"
        />
        <container-detail-facts
          v-if="!forbidden"
          :detail="detail"
          :forbidden="forbidden"
          :agent-id="agentId"
          :direct-address="directAddress"
        />
      </n-tab-pane>

      <!-- 历史曲线：view 权限，和实时指标同级别，不依赖详情的 manage 权限 -->
      <n-tab-pane name="metrics" tab="监控">
        <container-metric-history :agent-id="agentId" :container-id="metricContainerId" />
      </n-tab-pane>

      <!--
        检查：完整清单一页看完，供复制给同事或贴进工单。
        与概览同权限（manage）——清单里含挂载源路径与启动命令。
      -->
      <n-tab-pane name="inspect" tab="检查">
        <container-inspect-panel :detail="detail" :forbidden="forbidden" />
      </n-tab-pane>

      <n-tab-pane name="mounts" tab="挂载">
        <container-mounts-table :mounts="detail?.mounts ?? []" :forbidden="forbidden" />
      </n-tab-pane>

      <!-- 日志：view 权限，独立于详情的 manage 权限 -->
      <n-tab-pane name="logs" tab="日志">
        <container-detail-logs :agent-id="agentId" :container-id="containerId" :name="pageTitle" />
      </n-tab-pane>

      <!--
        控制台：在容器内执行命令（需要 manage）。
        这是"一次性执行"而不是远程终端，交互限制写在面板里，不在这里重复。
      -->
      <n-tab-pane name="console" tab="控制台">
        <container-console
          :agent-id="agentId"
          :container-id="containerId"
          :name="pageTitle"
          :online="agentOnline"
        />
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

/*
 * 操作区：贴一条浅底，和下面的标签页分开。
 * 不加边框与阴影，保持与页面其它区块一致的轻量观感。
 */
.container-detail__actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  padding: 10px 14px;
  background: var(--arkade-surface-bg);
  border-radius: 8px;
}

.container-detail__block-reason {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.container-detail__metrics {
  margin-bottom: 20px;
  padding-bottom: 16px;
  border-bottom: 1px solid var(--arkade-border-color);
}
</style>
