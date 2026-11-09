<script setup lang="ts">
/**
 * 服务器（Agent）列表页
 *
 * 职责边界：
 * - 数据来自 useAgentsStore（全局缓存 + 手动刷新，本期不做实时推送）；
 * - 行操作：查看容器、编辑、禁用/启用、删除；
 * - 「添加服务器」是一段说明，不是表单（原因见 AddAgentGuideModal）。
 *
 * 点击行进入容器页的取舍：离线服务器也允许进入（数据是 Hub 缓存的，仍可查看），
 * 只是页内的启停按钮会被禁用；如果连查看都不给，用户就完全看不到离线机器上的容器了。
 */
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NDataTable, NTag } from 'naive-ui'
import AddAgentGuideModal from '@/components/agent/AddAgentGuideModal.vue'
import AgentEditModal from '@/components/agent/AgentEditModal.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { agentRowProps, createAgentColumns } from '@/composables/agentColumns'
import { deleteAgent, disableAgent, enableAgent } from '@/api/agents'
import { useAgentsStore } from '@/stores/agents'
import type { Agent } from '@/types/agent'
import { ApiError } from '@/types/api'
import { agentDisplayName, isAgentDisabled } from '@/utils/agentDisplay'
import { confirmAction } from '@/utils/confirm'
import { toastError, toastSuccess, toastWarning } from '@/utils/feedback'

const store = useAgentsStore()
const router = useRouter()

/** 正在跳转的 agentId，用于按钮 loading，防止连点重复 push 路由 */
const enteringId = ref('')
/** 正在执行写操作的 agentId（删除/禁用/启用），用于行按钮 loading 与防连点 */
const busyId = ref('')

/** 编辑弹窗 */
const editShow = ref(false)
const editingAgent = ref<Agent | null>(null)

/** 添加服务器说明弹窗 */
const guideShow = ref(false)

const columns = createAgentColumns({
  onEnter: (agent: Agent) => enterAgent(agent),
  onEdit: (agent: Agent) => openEdit(agent),
  onToggleDisabled: (agent: Agent) => void handleToggleDisabled(agent),
  onDelete: (agent: Agent) => void handleDelete(agent),
  isBusy: (agentId: string) => enteringId.value === agentId || busyId.value === agentId
})

/** 顶部摘要：在线/禁用/总数，让用户一眼看到整体健康度 */
const summary = computed(() => {
  const total = store.agents.length
  const disabled = store.agents.filter((a) => isAgentDisabled(a)).length
  const disabledText = disabled > 0 ? `，已禁用 ${disabled} 台` : ''
  return `共 ${total} 台，在线 ${store.onlineCount} 台${disabledText}`
})

/** 进入某台服务器的容器列表 */
function enterAgent(agent: Agent): void {
  if (enteringId.value !== '') return
  enteringId.value = agent.id
  void router.push({ name: 'agent-containers', params: { id: agent.id } }).finally(() => {
    enteringId.value = ''
  })
}

/** 打开编辑弹窗 */
function openEdit(agent: Agent): void {
  editingAgent.value = agent
  editShow.value = true
}

/** 行点击：整行可点，但需要排除点击按钮/链接自身（它们已 stopPropagation） */
function handleRowProps(row: Agent): Record<string, unknown> {
  return {
    ...agentRowProps(row),
    style: 'cursor: pointer;',
    onClick: (e: MouseEvent) => {
      // 只在没有阻止冒泡的元素上触发行进入（按钮内部已 stopPropagation）
      const target = e.target
      if (target instanceof Element && target.closest('button, a')) return
      enterAgent(row)
    }
  }
}

/** 手动刷新（本期无 WebSocket 推送，靠用户主动刷新） */
async function handleRefresh(showToast: boolean): Promise<void> {
  const ok = await store.refresh()
  if (ok && showToast) {
    toastSuccess('服务器列表已刷新')
  }
}

/**
 * 删除服务器。
 *
 * 确认文案必须写清「仅移除纳管关系，不删服务器上的任何容器」——
 * 这条不写，用户根本不敢点（这是后端语义，也是最大的误解来源）。
 *
 * ## 关于「删除 vs 禁用」必须讲清楚
 *
 * 删除在线 Agent 后，后端会断开它的连接，Agent 会**重新注册**并再次出现在列表里。
 * 也就是说「删了又回来」是预期行为，不是 bug。
 * 如果用户真正想要的是「让这台服务器不能再接入」，正确操作是**禁用**而不是删除。
 * 不写这一条，用户会反复删除、反复看到它回来，然后以为系统坏了。
 *
 * 删除后还有两件事必须告诉用户：
 * 1. 后端返回的中文 message（明确说明未删除任何资产），直接展示，不自己重写；
 * 2. `orphan_stacks > 0` 时说明编排配置仍留在 Hub——
 *    注意这与编排列表里的 orphans 方向相反（那边是「Agent 有、Hub 没有」），
 *    不能混用「未纳管」这个词。
 */
async function handleDelete(agent: Agent): Promise<void> {
  if (busyId.value !== '') return
  const name = agentDisplayName(agent)

  const confirmed = await confirmAction({
    title: `确认移除服务器「${name}」？`,
    content:
      '此操作仅从 Arkade 中移除纳管关系，不会删除服务器上的任何容器。\n\n' +
      '服务器上正在运行的容器、镜像、网络与数据卷都保持不变，' +
      'Agent 也会继续在服务器上运行。\n\n' +
      '⚠️ 注意：如果这台服务器上的 Agent 仍在运行，它断开后会自动重新连接并再次出现在列表中。' +
      '如果你想让这台服务器不再接入 Arkade，请改用「禁用」而不是删除。',
    positiveText: '移除纳管',
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return

  busyId.value = agent.id
  try {
    const res = await deleteAgent(agent.id)

    // 优先展示后端给的说明：它才是「到底删了什么」的权威表述
    toastSuccess(res.message || `已移除服务器「${name}」的纳管关系`)

    if (res.orphan_stacks > 0) {
      /*
       * 用词必须区分两种「孤儿」，方向正好相反：
       * - 编排列表里的 orphans = Agent 上有、Hub 里没有（可通过编排页看到并纳管）；
       * - 这里的 orphan_stacks = Hub 里有、但所属服务器已被移除。
       *   删完服务器后界面上已经没有任何入口能访问到这些编排，
       *   所以**不能说「可在编排列表中查看」**——那是骗人的。
       * 正确的说法是：配置还在 Hub 里，服务器重新接入后会重新关联上。
       */
      toastWarning(
        `该服务器名下有 ${res.orphan_stacks} 个编排配置仍保留在 Hub 中，服务器重新接入后可继续访问。`
      )
    }

    await store.refresh()
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : '移除服务器失败')
  } finally {
    busyId.value = ''
  }
}

/**
 * 禁用 / 启用切换。
 *
 * 禁用涉及「之后无法操作这台服务器」，属于有影响的操作，因此要确认；
 * 启用是恢复性操作，无需确认直接执行。
 */
async function handleToggleDisabled(agent: Agent): Promise<void> {
  if (busyId.value !== '') return
  const name = agentDisplayName(agent)
  const disabled = isAgentDisabled(agent)

  if (!disabled) {
    const confirmed = await confirmAction({
      title: `确认禁用服务器「${name}」？`,
      content:
        '禁用后，该服务器的所有操作入口将不可用，无法在 Arkade 中管理其容器与编排。\n\n' +
        '不会删除任何容器，Agent 仍会保持运行；随时可以重新启用。',
      positiveText: '禁用',
      negativeText: '取消'
    })
    if (!confirmed) return
  }

  busyId.value = agent.id
  try {
    // 后端返回更新后的 Agent，直接替换本地那一行，不必重拉整表
    const updated = disabled ? await enableAgent(agent.id) : await disableAgent(agent.id)
    if (!store.replace(updated)) {
      // 本地没有这一行（异常情况），退回全量刷新保证界面正确
      await store.refresh()
    }
    toastSuccess(disabled ? `已启用服务器「${name}」` : `已禁用服务器「${name}」`)
  } catch (err) {
    toastError(err instanceof ApiError ? err.message : `${disabled ? '启用' : '禁用'}服务器失败`)
  } finally {
    busyId.value = ''
  }
}

/**
 * 编辑保存成功。
 *
 * PATCH 已经返回了更新后的完整对象，因此直接替换本地那一行即可，
 * 不重新拉整表——省一次请求，也避免列表「闪一下重新加载」的观感。
 * 若本地找不到该行（理论上不会），退化成一次全量刷新兜底。
 */
function handleSaved(updated: Agent): void {
  // replace 返回 false 说明本地没有这一行，此时靠刷新补齐
  if (!store.replace(updated)) {
    void handleRefresh(false)
  }
}

onMounted(() => {
  void handleRefresh(false)
})
</script>

<template>
  <div class="agent-list-page">
    <page-header title="服务器" :subtitle="summary">
      <template #actions>
        <n-button size="small" @click="guideShow = true">如何添加服务器</n-button>
        <n-button size="small" :loading="store.loading" @click="handleRefresh(true)">刷新</n-button>
      </template>
    </page-header>

    <!-- 加载失败：保留旧数据，用内联提示 + 可重试 -->
    <div v-if="store.errorText !== ''" class="page-alert">
      <n-alert type="error" closable>
        {{ store.errorText }}
      </n-alert>
      <n-button size="small" quaternary class="page-alert__retry" @click="handleRefresh(false)">
        重试
      </n-button>
    </div>

    <!--
      表格容器：窄屏（手机/平板）下表格列宽总和会超出视口，
      由 NDataTable 的 scroll-x 在内部横向滚动，容器负责兜住宽度。
      class 里的 min-width: 0 见 global.css 的 .arkade-table-scroll 说明。
    -->
    <div class="table-card arkade-table-scroll">
      <n-data-table
        v-if="store.agents.length > 0 || store.loading"
        :columns="columns"
        :data="store.agents"
        :loading="store.loading"
        :row-key="(row: Agent) => row.id"
        :row-props="handleRowProps"
        :bordered="false"
        :single-line="false"
        size="medium"
        :scroll-x="1180"
      />
      <empty-state v-else-if="store.loaded" description="还没有任何服务器接入。请在服务器上启动 Agent 并指向本 Hub。">
        <template #extra>
          <n-button size="small" type="primary" @click="guideShow = true">查看添加说明</n-button>
        </template>
      </empty-state>
      <!-- 首次加载完成前不显示空态，避免「闪一下没有数据」 -->
      <div v-else class="table-skeleton">
        <n-tag :bordered="false" size="small">正在加载服务器列表…</n-tag>
      </div>
    </div>

    <agent-edit-modal v-model:show="editShow" :agent="editingAgent" @saved="handleSaved" />
    <add-agent-guide-modal v-model:show="guideShow" />
  </div>
</template>

<style scoped>
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
}

/* 离线行整行置灰：仅做视觉弱化，拦截逻辑在 AgentRowActions 里 */
:deep(.agent-row--offline) {
  opacity: 0.55;
}

/* 名称列的可点击链接样式 */
:deep(.agent-name-link) {
  color: #2080f0;
  cursor: pointer;
  text-decoration: none;
}

:deep(.agent-name-link:hover) {
  text-decoration: underline;
}

:deep(.runtime-cell__version) {
  color: #8b9099;
  font-size: 12px;
}
</style>
