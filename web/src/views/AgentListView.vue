<script setup lang="ts">
/**
 * 服务器（Agent）列表页
 *
 * 职责边界：
 * - 数据来自 useAgentsStore（全局缓存 + 手动刷新，本期不做实时推送）；
 * - 点击行/按钮进入该服务器的容器列表；
 * - 离线行整行置灰、操作按钮 disabled + tooltip 说明原因。
 *
 * 点击行进入容器页的取舍：离线服务器也允许进入（数据是 Hub 缓存的，仍可查看），
 * 只是页内的启停按钮会被禁用；如果连查看都不给，用户就完全看不到离线机器上的容器了。
 */
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { NAlert, NButton, NDataTable, NTag } from 'naive-ui'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import { agentRowProps, createAgentColumns } from '@/composables/agentColumns'
import { useAgentsStore } from '@/stores/agents'
import type { Agent } from '@/types/agent'
import { toastSuccess } from '@/utils/feedback'

const store = useAgentsStore()
const router = useRouter()

/** 正在跳转的 agentId，用于按钮 loading，防止连点重复 push 路由 */
const enteringId = ref('')

const columns = createAgentColumns({
  onEnter: (agent: Agent) => enterAgent(agent),
  isBusy: (agentId: string) => enteringId.value === agentId
})

/** 顶部摘要：在线/总数，让用户一眼看到整体健康度 */
const summary = computed(() => {
  const total = store.agents.length
  return `共 ${total} 台，在线 ${store.onlineCount} 台`
})

/** 进入某台服务器的容器列表 */
function enterAgent(agent: Agent): void {
  if (enteringId.value !== '') return
  enteringId.value = agent.id
  void router.push({ name: 'agent-containers', params: { id: agent.id } }).finally(() => {
    enteringId.value = ''
  })
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

onMounted(() => {
  void handleRefresh(false)
})
</script>

<template>
  <div class="agent-list-page">
    <page-header title="服务器" :subtitle="summary">
      <template #actions>
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
        :scroll-x="960"
      />
      <empty-state v-else-if="store.loaded" description="还没有任何服务器接入。请在服务器上启动 Agent 并指向本 Hub。">
        <template #extra>
          <n-button size="small" @click="handleRefresh(false)">刷新</n-button>
        </template>
      </empty-state>
      <!-- 首次加载完成前不显示空态，避免「闪一下没有数据」 -->
      <div v-else class="table-skeleton">
        <n-tag :bordered="false" size="small">正在加载服务器列表…</n-tag>
      </div>
    </div>
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
