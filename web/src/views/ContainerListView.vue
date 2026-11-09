<script setup lang="ts">
/**
 * 容器列表页 /agents/:id
 *
 * 职责：
 * - 顶部显示所属服务器信息（名称、状态、引擎），服务器离线时给出显著提示；
 * - 表格展示容器，支持启动 / 停止 / 查看日志；
 * - 启停必须二次确认，成功后只局部更新该行状态并弹出提示（见 useContainers）。
 *
 * 数据来源拆分：
 * - Agent 信息来自 useAgentsStore；用户直接刷新本页时 store 可能为空，
 *   此时补拉一次服务器列表，保证能拿到在线状态（决定按钮是否可用）；
 * - 容器列表来自页面局部的 useContainers（页面卸载即释放）。
 */
import { computed, h, onMounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NDataTable, NSpace, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import AgentStatusTag from '@/components/agent/AgentStatusTag.vue'
import ContainerLogDrawer from '@/components/container/ContainerLogDrawer.vue'
import ContainerOriginTag from '@/components/container/ContainerOriginTag.vue'
import ContainerRowActions from '@/components/container/ContainerRowActions.vue'
import ContainerStateTag from '@/components/container/ContainerStateTag.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { useContainers } from '@/composables/useContainers'
import { usePolling } from '@/composables/usePolling'
import { useAgentsStore } from '@/stores/agents'
import type { Container } from '@/types/container'
import { isAgentOnline, needsForceDelete } from '@/utils/agentAction'
import { confirmAction, confirmWithOption } from '@/utils/confirm'
import { toastError, toastSuccess } from '@/utils/feedback'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'
import { mapRuntime } from '@/utils/statusMap'

const route = useRoute()
const router = useRouter()
const agentsStore = useAgentsStore()

/** 路由参数 id（服务器标识）；路由定义中为必填，这里统一成 string 便于 API 调用 */
const agentId = computed<string>(() => String(route.params.id ?? ''))

/** 当前服务器对象；可能为 undefined（直接刷新本页且 store 尚未加载时） */
const agent = computed(() => agentsStore.findById(agentId.value))

const { containers, loading, errorText, loaded, busy, load, runAction, removeContainer } = useContainers(
  () => agentId.value
)

/** 日志抽屉：show 控制显隐，container 记录当前查看的容器 */
const logDrawerShow = ref(false)
const logContainer = ref<Container | null>(null)

/**
 * 是否允许向该服务器下发操作。
 * agent 信息未知时按「不可操作」处理：宁可等列表加载完，也不发一个可能必然失败的请求。
 */
const online = computed(() => isAgentOnline(agent.value))

/** 页面标题：优先服务器名称，未知时退回 id */
const title = computed(() => agent.value?.name ?? agentId.value)

/** 顶部摘要 */
const summary = computed(() => {
  const total = containers.value.length
  const running = containers.value.filter((c) => c.state === 'running').length
  return `共 ${total} 个容器，运行中 ${running} 个`
})

/** 服务器离线提示里带上次心跳时间，帮助用户判断是刚掉线还是离线很久 */
const lastSeenText = computed(() => {
  if (!agent.value) return ''
  return `${formatRelativeTime(agent.value.last_seen)}（${formatAbsoluteTime(agent.value.last_seen)}）`
})

/**
 * 表格列。
 *
 * 用 h() 渲染而非模板：Naive UI 的列表格列需要 render 函数，
 * 所有交互都通过下面的 handler 交给页面逻辑，列定义本身保持无副作用。
 */
const columns = computed<DataTableColumns<Container>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 160,
    ellipsis: { tooltip: true },
    render: (row) => row.name || row.id
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
    title: '来源',
    key: 'origin',
    width: 110,
    render: (row) => h(ContainerOriginTag, { origin: row.origin })
  },
  {
    title: '端口',
    key: 'ports',
    minWidth: 220,
    ellipsis: { tooltip: true },
    render: (row) => row.ports || '—'
  },
  {
    title: '操作',
    key: 'actions',
    // 5 个按钮横排需要更大宽度；窄屏由 scroll-x 横向滚动兜底
    width: 300,
    fixed: 'right',
    render: (row) =>
      h(ContainerRowActions, {
        agent: agent.value,
        container: row,
        busy: busy.value[row.id] === true,
        onStart: () => void handleStart(row),
        onStop: () => void handleStop(row),
        onRestart: () => void handleRestart(row),
        onLogs: () => openLogs(row),
        onDelete: () => void handleDelete(row)
      })
  }
])

/** 打开日志抽屉 */
function openLogs(container: Container): void {
  logContainer.value = container
  logDrawerShow.value = true
}

/**
 * 启动容器：二次确认 → 调接口 → 局部更新行 + 提示。
 *
 * 二次确认的理由：启动会改变线上服务状态，误点代价高；
 * 确认文案里写清服务器名与容器名，避免用户在多个标签页里点错对象。
 */
async function handleStart(container: Container): Promise<void> {
  if (!online.value) return
  const confirmed = await confirmAction({
    title: '启动容器',
    content: `确认在服务器「${title.value}」上启动容器「${container.name}」？`,
    positiveText: '启动',
    danger: false
  })
  if (!confirmed) return

  const result = await runAction(container, 'start')
  if (result.ok) {
    toastSuccess(result.message)
  } else {
    toastError(result.message)
  }
}

/**
 * 停止容器：二次确认 → 调接口 → 局部更新行 + 提示。
 * 停止是危险操作，对话框用 warning 样式（红色确认按钮）。
 */
async function handleStop(container: Container): Promise<void> {
  if (!online.value) return
  const confirmed = await confirmAction({
    title: '停止容器',
    content: `确认停止服务器「${title.value}」上的容器「${container.name}」？该容器的服务将中断。`,
    positiveText: '停止',
    danger: true
  })
  if (!confirmed) return

  const result = await runAction(container, 'stop')
  if (result.ok) {
    toastSuccess(result.message)
  } else {
    toastError(result.message)
  }
}

/**
 * 重启容器。
 *
 * 不弹确认：重启是「恢复性」操作且可重复执行，误点代价低（服务会短暂中断）；
 * 相比之下停止/删除才需要二次确认。若这里也弹窗，常用操作会被打断得很难受。
 */
async function handleRestart(container: Container): Promise<void> {
  if (!online.value) return
  const result = await runAction(container, 'restart')
  if (result.ok) {
    toastSuccess(result.message)
  } else {
    toastError(result.message)
  }
}

/**
 * 删除容器。
 *
 * 三个必须讲清的点：
 * 1. **不可恢复**：容器删掉就没了，不能像停止那样再启动回来；
 * 2. **运行中的容器要强制停止**：不说明的话用户会奇怪「为什么还能删正在跑的」；
 * 3. **匿名数据卷默认保留**：删卷是数据丢失，必须由用户主动勾选。
 *    这与编排删除、存储卷删除遵循同一条原则：危险默认关闭。
 */
async function handleDelete(container: Container): Promise<void> {
  if (!online.value) return
  const running = needsForceDelete(container.state)

  const result = await confirmWithOption({
    title: `确认删除容器「${container.name}」？`,
    content: running
      ? '该容器正在运行，将强制停止并删除。\n\n删除后容器无法恢复。'
      : '删除后容器无法恢复。',
    optionLabel: '同时删除匿名数据卷（数据将永久丢失）',
    optionDefault: false,
    optionHint: '⚠️ 容器使用的匿名数据卷将一并删除，卷内数据永久丢失且无法恢复。',
    positiveText: '删除',
    negativeText: '取消',
    danger: true
  })
  if (!result.confirmed) return

  const res = await removeContainer(container, {
    // 运行中的容器必须 force，否则后端会拒绝
    force: running,
    removeVolumes: result.optionChecked
  })
  if (res.ok) {
    toastSuccess(res.message)
  } else {
    toastError(res.message)
  }
}

/** 手动刷新容器列表 */
async function handleRefresh(showToast: boolean): Promise<void> {  // 顺带刷新服务器状态：用户可能在别处把服务器重启/下线了
  void agentsStore.refresh()
  const ok = await load()
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
      表格容器：窄屏下六列（名称/镜像/状态/来源/端口/操作）总宽超出视口，
      由 NDataTable 的 scroll-x 在内部横向滚动；容器上的 min-width: 0
      见 global.css 的 .arkade-table-scroll 说明（缺了它横向滚动会失效）。
    -->
    <div class="table-card arkade-table-scroll">
      <n-data-table
        v-if="containers.length > 0 || loading"
        :columns="columns"
        :data="containers"
        :loading="loading"
        :row-key="(row: Container) => row.id"
        :bordered="false"
        :single-line="false"
        size="medium"
        :scroll-x="1080"
      />
      <empty-state
        v-else-if="loaded"
        :description="online ? '该服务器上还没有容器' : '服务器离线，无法获取容器列表'"
      >
        <template #extra>
          <n-button size="small" @click="handleRefresh(false)">刷新</n-button>
        </template>
      </empty-state>
      <div v-else class="table-skeleton">正在加载容器列表…</div>
    </div>

    <container-log-drawer
      v-model:show="logDrawerShow"
      :agent-id="agentId"
      :container="logContainer"
    />
  </div>
</template>

<style scoped>
.agent-meta {
  margin-bottom: 12px;
  padding: 10px 14px;
  background: #fff;
  border-radius: 8px;
  font-size: 13px;
  color: #4a4f56;
}

.agent-meta__label {
  color: #8b9099;
}

.agent-meta__value {
  color: #1f2225;
}

.agent-meta__version {
  color: #8b9099;
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

.table-card {
  background: #fff;
  border-radius: 8px;
  padding: 8px 8px 0;
  box-shadow: 0 1px 2px rgba(31, 34, 37, 0.04);
}

.table-skeleton {
  padding: 48px 0;
  text-align: center;
  color: #8b9099;
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
  color: #8b9099;
}
</style>
