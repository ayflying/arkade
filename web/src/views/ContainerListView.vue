<script setup lang="ts">
/**
 * 容器列表页 /agents/:id
 *
 * 职责（本轮调整后）：
 * - 顶部显示所属服务器信息（名称、状态、引擎），服务器离线时给出显著提示；
 * - 表格只做「浏览 + 勾选」：点名称进详情，单个容器的启停/重启/删除/重建
 *   全部收到详情页里，列表上不再放行内按钮；
 * - 支持多选后批量执行启动 / 停止 / 重启 / 删除 / 重建；
 * - 支持按名称、镜像、ID 模糊搜索。
 *
 * 为什么把行内操作收走：
 * 容器有 5 个操作按钮时，"操作"列要占 300px，窄屏只能横向滚动才能点；
 * 而且每个按钮都紧贴相邻行，误点概率高。操作集中到详情页后，
 * 列表可以专心承载"我在这一堆容器里找谁"这件事——所以同时补了搜索与多选。
 *
 * 数据来源拆分：
 * - Agent 信息来自 useAgentsStore；用户直接刷新本页时 store 可能为空，
 *   此时补拉一次服务器列表，保证能拿到在线状态（决定按钮是否可用）；
 * - 容器列表来自页面局部的 useContainers（页面卸载即释放）。
 */
import { computed, h, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NDataTable, NInput, NSpace, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import AgentStatusTag from '@/components/agent/AgentStatusTag.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import TablePager from '@/components/common/TablePager.vue'
import ContainerMetricCell from '@/components/container/ContainerMetricCell.vue'
import ContainerPortCell from '@/components/container/ContainerPortCell.vue'
import ContainerStateTag from '@/components/container/ContainerStateTag.vue'
import { useContainerMetrics } from '@/composables/useContainerMetrics'
import { useContainers } from '@/composables/useContainers'
import type { BatchOperation, BatchOutcome } from '@/composables/useContainers'
import { clampPage, slicePage } from '@/composables/useLocalPager'
import { usePolling } from '@/composables/usePolling'
import { useAgentsStore } from '@/stores/agents'
import type { Container } from '@/types/container'
import { canRestartContainer, canStartContainer, canStopContainer, isAgentOnline } from '@/utils/agentAction'
import { confirmAction, confirmWithOption } from '@/utils/confirm'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'
import { useResponsiveColumns } from '@/utils/responsiveColumns'
import { mapRuntime } from '@/utils/statusMap'

const route = useRoute()
const router = useRouter()
const agentsStore = useAgentsStore()

/** 路由参数 id（服务器标识）；路由定义中为必填，这里统一成 string 便于 API 调用 */
const agentId = computed<string>(() => String(route.params.id ?? ''))

/** 当前服务器对象；可能为 undefined（直接刷新本页且 store 尚未加载时） */
const agent = computed(() => agentsStore.findById(agentId.value))

/**
 * 服务器访问地址（完整地址，含 http/https 前缀）；未配置时为空串。
 *
 * 端口链接靠它把 `0.0.0.0` 这类通配绑定换成真正可访问的地址，
 * 所以为空时**不生成任何链接**——见 utils/portLinks 的规则。
 */
const currentAgentDirectAddress = computed<string>(() => agent.value?.direct_address ?? '')

/** 进入容器详情（单个容器的所有操作都在那里） */
function goDetail(containerId: string): void {
  if (agentId.value === '' || containerId === '') return
  void router.push({
    name: 'agent-container-detail',
    params: { id: agentId.value, containerId }
  })
}

const { containers, loading, errorText, loaded, load, runBatch } = useContainers(() => agentId.value)

/**
 * 是否允许向该服务器下发操作。
 * agent 信息未知时按「不可操作」处理：宁可等列表加载完，也不发一个可能必然失败的请求。
 */
const online = computed(() => isAgentOnline(agent.value))
const hasContainers = computed(() => containers.value.length > 0)
const metrics = useContainerMetrics(agentId, hasContainers)

function renderMetricCell(row: Container, group: 'resources' | 'network') {
  return h(ContainerMetricCell, {
    group,
    sample: metrics.byId.value.get(row.id),
    stale: metrics.byStaleId.value.has(row.id),
    emptyStatus: metrics.emptyStatus.value
  })
}

/** 页面标题：优先服务器名称，未知时退回 id */
const title = computed(() => agent.value?.name ?? agentId.value)

/* ───────────────────────── 模糊搜索 ───────────────────────── */

/**
 * 搜索关键词。
 *
 * 匹配名称、镜像、ID 三段：用户找容器时可能记得其中任何一个
 * （"那个 nginx 的"、"web-1"、"3f2a 开头那个"）。
 * 不区分大小写、不做分词——容器名的形态是 `web-1`、`db_primary` 这类短串，
 * 子串匹配比模糊分词更符合直觉，也不会把 `web` 匹配到 `webhook` 之外的东西。
 */
const keyword = ref('')

const filteredContainers = computed<Container[]>(() => {
  const q = keyword.value.trim().toLowerCase()
  if (q === '') return containers.value
  return containers.value.filter((c) => {
    return (
      c.name.toLowerCase().includes(q) ||
      c.image.toLowerCase().includes(q) ||
      c.id.toLowerCase().includes(q)
    )
  })
})

/** 顶部摘要：搜索时同时给出"命中几个"，否则用户以为容器丢了 */
const summary = computed(() => {
  const total = containers.value.length
  const running = containers.value.filter((c) => c.state === 'running').length
  const base = `共 ${total} 个容器，运行中 ${running} 个`
  const q = keyword.value.trim()
  if (q === '') return base
  return `${base}；匹配「${q}」的 ${filteredContainers.value.length} 个`
})

/** 服务器离线提示里带上次心跳时间，帮助用户判断是刚掉线还是离线很久 */
const lastSeenText = computed(() => {
  if (!agent.value) return ''
  return `${formatRelativeTime(agent.value.last_seen)}（${formatAbsoluteTime(agent.value.last_seen)}）`
})

/* ───────────────────────── 多选与批量 ───────────────────────── */

/**
 * 已勾选的容器 ID。
 *
 * 用 ID 而不是行对象：轮询每 5 秒会替换整个数组，
 * 持有行对象会让勾选状态在刷新后失效（对象引用变了）。
 */
const checkedIds = ref<string[]>([])

/**
 * 勾选结果 → 容器对象。
 *
 * 只认当前列表里还存在的 ID：重建会换 ID，轮询会把旧行换掉，
 * 那时残留的 ID 不应该被当作可操作对象。
 */
const checkedContainers = computed<Container[]>(() => {
  const set = new Set(checkedIds.value)
  return containers.value.filter((c) => set.has(c.id))
})

const checkedCount = computed(() => checkedContainers.value.length)

/** 批量动作进行中：整条工具栏禁用，避免重复提交 */
const batchBusy = ref(false)

/**
 * 行级复选框的可勾选条件。
 *
 * 一律允许勾选：批量里"启动一个已在运行的"是幂等无害的，
 * 真正不能做的动作由每个按钮的可用状态与执行结果来体现。
 * 若在这里按状态禁用某一行的勾选，用户会困惑"为什么这个不能选"，
 * 而他其实只是想对整批执行"停止"。
 */
const rowSelectable = (): boolean => true

function handleCheckedChange(keys: Array<string | number>): void {
  checkedIds.value = keys.map((k) => String(k))
}

/** 清空勾选（批量结束或切换服务器时） */
function clearChecked(): void {
  checkedIds.value = []
}

/**
 * 批量启动/停止/重启的可用性。
 *
 * 只要勾选里存在一个"可以做这件事"的容器就允许点：
 * 混合状态（有运行有停止）时用户往往就是想"全停"或"全启"，
 * 这时禁用按钮反而挡住了最常见的用法。不可做的那些项在结果里会显示原因。
 */
const canBatchStart = computed(
  () => online.value && checkedContainers.value.some((c) => canStartContainer(c.state))
)
const canBatchStop = computed(
  () => online.value && checkedContainers.value.some((c) => canStopContainer(c.state))
)
const canBatchRestart = computed(
  () => online.value && checkedContainers.value.some((c) => canRestartContainer(c.state))
)
const canBatchDelete = computed(() => online.value && checkedContainers.value.length > 0)
const canBatchRecreate = computed(() => online.value && checkedContainers.value.length > 0)

/** 批量操作的显示名（确认框与结果提示共用，避免两处文案走样） */
const BATCH_LABELS: Record<BatchOperation, string> = {
  start: '启动',
  stop: '停止',
  restart: '重启',
  remove: '删除',
  recreate: '重建'
}

/**
 * 汇总批量结果并提示。
 *
 * 全成功 → 成功提示；有失败 → 用警告提示并列出失败的容器名与原因。
 * 不做"失败就静默"：批量最怕的就是用户以为都成功了。
 */
function reportBatchOutcome(outcome: BatchOutcome, action: BatchOperation): void {
  const label = BATCH_LABELS[action]
  if (outcome.failed.length === 0) {
    toastSuccess(`已${label} ${outcome.ok} 个容器`)
    return
  }
  const detail = outcome.failed
    .slice(0, 5)
    .map((f) => `${f.name}: ${f.message}`)
    .join('；')
  const more = outcome.failed.length > 5 ? `；等 ${outcome.failed.length} 个` : ''
  if (outcome.ok === 0) {
    toastError(`${label}失败：${detail}${more}`)
  } else {
    toastWarning(`已${label} ${outcome.ok} 个，${outcome.failed.length} 个失败：${detail}${more}`)
  }
}

/** 批量启动 */
async function handleBatchStart(): Promise<void> {
  if (!canBatchStart.value) return
  const confirmed = await confirmAction({
    title: '批量启动容器',
    content: `确认在服务器「${title.value}」上启动已勾选的 ${checkedCount.value} 个容器？`,
    positiveText: '启动',
    danger: false
  })
  if (!confirmed) return
  await execBatch('start')
}

/** 批量停止 */
async function handleBatchStop(): Promise<void> {
  if (!canBatchStop.value) return
  const confirmed = await confirmAction({
    title: '批量停止容器',
    content: `确认停止服务器「${title.value}」上已勾选的 ${checkedCount.value} 个容器？这些容器的服务将中断。`,
    positiveText: '停止',
    danger: true
  })
  if (!confirmed) return
  await execBatch('stop')
}

/**
 * 批量重启。
 *
 * 不弹确认，与单个重启的理由一致：重启是「恢复性」且可重复的操作，
 * 批量场景下用户往往就是要"全刷一遍"，多一次弹窗只是多一次点击。
 */
async function handleBatchRestart(): Promise<void> {
  if (!canBatchRestart.value) return
  await execBatch('restart')
}

/**
 * 批量删除。
 *
 * 确认框只说"删除、不可恢复"，**不提供删卷勾选**：
 * 数据丢失必须逐次显式确认，不能藏在批量动作背后。
 * 这一点要在文案里讲明，否则用户会以为勾了批量就等于连卷一起删。
 */
async function handleBatchDelete(): Promise<void> {
  if (!canBatchDelete.value) return
  const result = await confirmWithOption({
    title: `确认删除 ${checkedCount.value} 个容器？`,
    content: `服务器「${title.value}」上已勾选的容器将被删除，运行中的会先被强制停止。`,
    notes: ['删除后容器无法恢复，且批量删除不会移除容器的数据卷。'],
    optionLabel: '我确认要删除这些容器',
    optionDefault: false,
    positiveText: '删除',
    negativeText: '取消',
    danger: true
  })
  // 批量删除要求勾选确认项：数量多、且不可撤销，多一道确认不算苛刻。
  if (!result.confirmed || !result.optionChecked) return
  await execBatch('remove')
}

/**
 * 批量重建。
 *
 * 默认**不拉镜像**：拉取耗时不可控，一批里卡住一个会让整批看起来"没反应"。
 * 需要拉最新镜像时到详情页单独重建并勾选「重新拉取镜像」。
 */
async function handleBatchRecreate(): Promise<void> {
  if (!canBatchRecreate.value) return
  const result = await confirmWithOption({
    title: `确认重建 ${checkedCount.value} 个容器？`,
    content: `将按各自原配置删除并重新创建这 ${checkedCount.value} 个容器，逐个串行执行。`,
    notes: [
      '重建会换掉容器对象：容器 ID 变化、日志从头开始、容器内未挂载到卷的数据会丢失。',
      '挂载的目录与数据卷会沿用原配置，卷内数据保留。',
      '本次批量重建不会拉取新镜像；需要更新镜像请到容器详情里单独重建并勾选「重新拉取镜像」。'
    ],
    optionLabel: '我确认要重建这些容器',
    optionDefault: false,
    positiveText: '重建',
    negativeText: '取消',
    danger: true
  })
  if (!result.confirmed || !result.optionChecked) return
  await execBatch('recreate')
}

/** 真正执行批量：设忙碌标记 → 调组合式函数 → 汇总提示 → 清勾选 */
async function execBatch(action: BatchOperation): Promise<void> {
  batchBusy.value = true
  try {
    const targets = checkedContainers.value
    const outcome = await runBatch(targets, action)
    reportBatchOutcome(outcome, action)
    // 重建后 ID 已变，旧的勾选全部失效；统一清空，避免"看着还勾着其实指向已删容器"
    clearChecked()
  } finally {
    batchBusy.value = false
  }
}

/* ───────────────────────── 表格列 ───────────────────────── */

/**
 * 表格列。
 *
 * 用 h() 渲染而非模板：Naive UI 的列表格列需要 render 函数，
 * 所有交互都通过下面的 handler 交给页面逻辑，列定义本身保持无副作用。
 *
 * 勾选列放在最左（fixed），方便窄屏横向滚动时始终能勾；
 * 操作列已移除，容器操作统一在详情页。
 */
const columns = useResponsiveColumns(computed<DataTableColumns<Container>>(() => [
  {
    type: 'selection',
    // 勾选列固定在最左：横向滚动到端口/指标区域时仍然能勾
    fixed: 'left',
    width: 44,
    disabled: () => !rowSelectable()
  },
  {
    title: '名称',
    key: 'name',
    minWidth: 180,
    ellipsis: { tooltip: true },
    /*
     * 名称可点击进详情。详情页承载全部操作，
     * 因此这里既是"看详情"的入口，也是"操作容器"的入口。
     */
    render: (row) =>
      h(
        'a',
        { class: 'arkade-link arkade-cell-name', onClick: () => goDetail(row.id) },
        row.name !== '' ? row.name : row.id
      )
  },
  {
    title: '镜像',
    key: 'image',
    minWidth: 220,
    ellipsis: { tooltip: true },
    render: (row) => row.image || '—'
  },
  {
    title: '状态',
    key: 'state',
    width: 170,
    render: (row) =>
      h('span', { class: 'state-cell' }, [
        h(ContainerStateTag, { state: row.state }),
        // 后端 status 是 "Up 3 hours" 这类运行时描述，作为补充信息展示
        row.status !== '' ? h('span', { class: 'state-cell__detail' }, row.status) : null
      ])
  },
  {
    title: '资源用量',
    key: 'resourceMetrics',
    width: 146,
    render: (row) => renderMetricCell(row, 'resources')
  },
  {
    title: '网络速率',
    key: 'networkMetrics',
    width: 156,
    render: (row) => renderMetricCell(row, 'network')
  },
  {
    title: '端口',
    key: 'ports',
    minWidth: 220,
    /*
     * 不再显示后端原始的 ports 文本：其中 0.0.0.0 是通配绑定，
     * 拼成链接必然打不开。改为按结构化 published_ports 渲染，
     * 只有确定可达的才做成链接（规则见 utils/portLinks）。
     */
    render: (row) =>
      h(ContainerPortCell, {
        container: row,
        agentId: agentId.value,
        directAddress: currentAgentDirectAddress.value
      })
  }
]))

/** 手动刷新容器列表 */
async function handleRefresh(showToast: boolean): Promise<void> {
  // 顺带刷新服务器状态：用户可能在别处把服务器重启/下线了
  void agentsStore.refresh()
  const [, ok] = await Promise.all([metrics.refresh(), load()])
  if (ok && showToast) {
    toastSuccess('容器列表已刷新')
  }
}

/** 返回服务器列表 */
function goBack(): void {
  void router.push({ name: 'agents' })
}

/**
 * 状态自动刷新：每 5 秒静默拉一次容器列表。
 *
 * 为什么 5 秒：容器启停/退出是用户最关心的变化，间隔太长会觉得「没反应」；
 * 5 秒既够及时，单台服务器一次列表请求的开销也很小。
 *
 * 静默模式（load(true)）不显示骨架，避免页面每 5 秒闪一次。
 * 暂停条件（页面隐藏、全局开关关闭）都由 usePolling 统一处理。
 */
usePolling(
  async () => {
    // 服务器离线时拉列表必然失败，白费一次请求还会累积失败计数
    if (!online.value) return
    await load(true)
  },
  {
    name: 'container-list',
    interval: 5000,
    // 首屏由 onMounted 的 load() 负责，这里不重复拉
    immediate: false
  }
)

onMounted(async () => {
  // 直接刷新本页时 store 为空：先补一次服务器列表，否则无法知道是否在线
  if (agentsStore.agents.length === 0) {
    void agentsStore.refresh()
  }
  await load()
})

/* ───────────────────────── 分页 ───────────────────────── */

/**
 * 分页：每页 20 个容器（TABLE_PAGE_SIZE）。
 *
 * 关键约束：分页**只影响渲染**。
 * - 列表轮询（usePolling → load）与指标采样（useContainerMetrics）都基于全量 containers，
 *   改成后端分页会切断这两者；这里的轮询会把最新数据写回全量数组，
 *   当前页内容随之自动更新，不会出现「翻页后状态不再刷新」。
 * - 顶部摘要「共 52 个容器，运行中 20 个」统计的是全量，不是当前页。
 * - 搜索在分页之前生效：搜索结果也分页，页码从 1 开始。
 *
 * 越界处理：52 个容器停在第 3 页时删掉几个只剩 40 个，页码会越界，
 * watch 负责夹回最后一页（而不是显示空表格）。
 */
const page = ref(1)
const pagedContainers = computed(() => slicePage(filteredContainers.value, page.value))

watch(
  () => filteredContainers.value.length,
  (count) => {
    page.value = clampPage(page.value, count)
  }
)

/** 输入关键词时回到第 1 页：否则可能停在一个空页上，看起来像"没搜到" */
watch(keyword, () => {
  page.value = 1
})

/** 切换服务器回到第 1 页并清空勾选：另一台机器是另一批容器 */
watch(agentId, () => {
  page.value = 1
  clearChecked()
})
</script>

<template>
  <div class="container-list-page">
    <page-header :title="title" :subtitle="summary">
      <template #actions>
        <n-button size="small" quaternary @click="goBack">返回</n-button>
        <n-button size="small" :loading="loading" @click="handleRefresh(true)">刷新</n-button>
      </template>
    </page-header>

    <!-- 服务器信息条：把在线状态与引擎版本摆在标题下方，替代「进页面才知道离不离线」 -->
    <div class="agent-meta">
      <n-space align="center" :size="12">
        <span class="agent-meta__label">服务器状态</span>
        <agent-status-tag v-if="agent" :status="agent.status" />
        <n-tag v-else size="small" :bordered="false">信息未加载</n-tag>

        <span class="agent-meta__label">引擎</span>
        <span class="agent-meta__value">
          {{ agent ? mapRuntime(agent.runtime) : '—' }}
          <span v-if="agent?.runtime_version" class="agent-meta__version">{{ agent.runtime_version }}</span>
        </span>

        <span class="agent-meta__label">Agent 版本</span>
        <span class="agent-meta__value">{{ agent?.version || '—' }}</span>

        <span class="agent-meta__label">最后心跳</span>
        <span class="agent-meta__value">{{ agent ? lastSeenText : '—' }}</span>
      </n-space>
    </div>

    <!-- 离线警示：明确告诉用户「操作会失败」，而不是让按钮静默变灰 -->
    <n-alert v-if="agent && !online" type="warning" class="page-alert" :bordered="false">
      服务器当前不在线，所有容器操作已禁用。请确认 Agent 进程是否在运行，或网络是否可达。
    </n-alert>

    <div v-if="errorText !== ''" class="page-alert">
      <n-alert type="error" closable>
        {{ errorText }}
      </n-alert>
      <n-button size="small" quaternary class="page-alert__retry" @click="handleRefresh(false)">
        重试
      </n-button>
    </div>

    <!--
      工具栏：搜索 + 批量操作。
      搜索常驻（找容器是高频动作）；批量按钮只在有勾选时出现，
      没勾选时显示一行提示，避免一排灰按钮让人以为功能坏了。
    -->
    <div class="list-toolbar">
      <n-input
        v-model:value="keyword"
        class="list-toolbar__search"
        size="small"
        clearable
        placeholder="搜索名称 / 镜像 / ID"
      />
      <div v-if="checkedCount > 0" class="list-toolbar__actions">
        <span class="list-toolbar__count">已选 {{ checkedCount }} 个</span>
        <n-button
          size="small"
          :disabled="!canBatchStart || batchBusy"
          :loading="batchBusy"
          @click="handleBatchStart"
        >
          启动
        </n-button>
        <n-button
          size="small"
          :disabled="!canBatchStop || batchBusy"
          :loading="batchBusy"
          @click="handleBatchStop"
        >
          停止
        </n-button>
        <n-button
          size="small"
          :disabled="!canBatchRestart || batchBusy"
          :loading="batchBusy"
          @click="handleBatchRestart"
        >
          重启
        </n-button>
        <n-button
          size="small"
          :disabled="!canBatchRecreate || batchBusy"
          :loading="batchBusy"
          @click="handleBatchRecreate"
        >
          重建
        </n-button>
        <n-button
          size="small"
          type="error"
          secondary
          :disabled="!canBatchDelete || batchBusy"
          :loading="batchBusy"
          @click="handleBatchDelete"
        >
          删除
        </n-button>
        <n-button size="small" quaternary :disabled="batchBusy" @click="clearChecked">
          取消选择
        </n-button>
      </div>
      <span v-else class="list-toolbar__hint">
        勾选容器后可批量启动 / 停止 / 重启 / 删除 / 重建；单个容器的操作在详情页
      </span>
    </div>

    <!--
      表格容器：窄屏下列宽总和超出视口，
      由 NDataTable 的 scroll-x 在内部横向滚动；容器上的 min-width: 0
      见 global.css 的 .arkade-table-scroll 说明（缺了它横向滚动会失效）。
    -->
    <div class="table-card arkade-table-scroll">
      <p v-if="containers.length > 0" class="arkade-table-scroll__hint">
        左右滑动查看容器指标与端口
      </p>
      <n-data-table
        v-if="containers.length > 0 || loading"
        :columns="columns"
        :data="pagedContainers"
        :loading="loading"
        :row-key="(row: Container) => row.id"
        :checked-row-keys="checkedIds"
        :bordered="false"
        :single-line="false"
        size="medium"
        :scroll-x="1182"
        @update:checked-row-keys="handleCheckedChange"
      />
      <!--
        搜索无结果与"这台机器没有容器"是两种完全不同的情况：
        前者要提示清空搜索，后者提示去部署。混用一句话会让用户
        以为自己搜错了，或者以为容器被删了。
      -->
      <empty-state
        v-else-if="loaded && filteredContainers.length === 0 && keyword.trim() !== ''"
        :description="`没有匹配「${keyword.trim()}」的容器`"
      >
        <template #extra>
          <n-button size="small" @click="keyword = ''">清空搜索</n-button>
        </template>
      </empty-state>
      <empty-state
        v-else-if="loaded"
        :description="online ? '该服务器上还没有容器' : '服务器离线，无法获取容器列表'"
      >
        <template #extra>
          <n-button size="small" @click="handleRefresh(false)">刷新</n-button>
        </template>
      </empty-state>
      <div v-else class="table-skeleton">正在加载容器列表…</div>

      <!--
        分页条：容器多时（如 52 个）一页只显示 20 个。
        放在卡片内部、表格之后：分页翻的是这张表，跟着卡片收边更清楚。
      -->
      <table-pager
        v-if="filteredContainers.length > 0"
        v-model:page="page"
        :item-count="filteredContainers.length"
        unit="个"
      />
    </div>
  </div>
</template>

<style scoped>
.agent-meta {
  margin-bottom: 12px;
  padding: 10px 14px;
  background: var(--arkade-surface-bg);
  border-radius: 8px;
  font-size: 13px;
  color: var(--arkade-text-secondary);
}

.agent-meta__label {
  color: var(--arkade-text-tertiary);
}

.agent-meta__value {
  color: var(--arkade-text-primary);
}

.agent-meta__version {
  color: var(--arkade-text-tertiary);
  margin-left: 4px;
}

.page-alert {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-bottom: 16px;
}

.page-alert :deep(.n-alert) {
  flex: 1;
}

.page-alert__retry {
  flex-shrink: 0;
}

/*
 * 工具栏：搜索固定宽度靠左，批量按钮跟随在右。
 * 用 flex-wrap 让窄屏时按钮换行而不是被挤出视口——
 * 批量按钮一共 6 个，窄屏硬排会把"删除"挤到看不见的地方。
 */
.list-toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  margin-bottom: 12px;
}

.list-toolbar__search {
  width: 260px;
  flex-shrink: 0;
}

.list-toolbar__actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.list-toolbar__count {
  font-size: 13px;
  color: var(--arkade-text-secondary);
  margin-right: 2px;
}

.list-toolbar__hint {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.table-card {
  background: var(--arkade-surface-bg);
  border-radius: 8px;
  padding: 8px 8px 0;
  box-shadow: 0 1px 2px rgba(31, 34, 37, 0.04);
}

.table-skeleton {
  padding: 48px 0;
  text-align: center;
  color: var(--arkade-text-tertiary);
}

/* 状态列：徽标 + 后端运行时描述两行展示 */
:deep(.state-cell) {
  display: inline-flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
}

:deep(.state-cell__detail) {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}
</style>
