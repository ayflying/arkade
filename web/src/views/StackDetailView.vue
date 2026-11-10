<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NAlert, NButton, NSpin, NTabPane, NTabs, NTag } from 'naive-ui'
import PageHeader from '@/components/common/PageHeader.vue'
import StackContainersTable from '@/components/stack/StackContainersTable.vue'
import StackContainerLogs from '@/components/stack/StackContainerLogs.vue'
import ManagedStackConfigPanel from '@/components/stack/ManagedStackConfigPanel.vue'
import ExternalStackConfigSection from '@/components/stack/ExternalStackConfigSection.vue'
import ManagedStackActions from '@/components/stack/ManagedStackActions.vue'
import StackStateTag from '@/components/stack/StackStateTag.vue'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { useStackDetail } from '@/composables/useStackDetail'
import type { StackState } from '@/types/stack'

const route = useRoute(), router = useRouter()
const { agentId, agent, displayName, isOnline } = useCurrentAgent()
const stackName = computed(() => String(route.params.name ?? ''))
const target = () => [agentId.value ?? '', stackName.value] as const
const detail = useStackDetail(target)
const stack = detail.stack
const activeTab = ref('containers')
const projectKey = computed(() => JSON.stringify(target()))
const state = computed<StackState>(() => {
  const value = stack.value?.status
  return ['running', 'stopped', 'partial', 'offline', 'busy'].includes(value ?? '') ? value as StackState : 'unknown'
})
const managed = computed(() => stack.value?.managed === true ? stack.value : null)
watch(projectKey, () => { activeTab.value = 'containers' })
function back() { void router.push({ name: 'agent-stacks', params: { id: agentId.value ?? '' } }) }
</script>

<template>
  <div class="stack-detail">
    <page-header :title="stackName" :subtitle="`编排详情 · ${displayName}`">
      <template #actions>
        <n-button size="small" @click="back">返回编排列表</n-button>
        <n-button size="small" :loading="detail.loading.value" @click="detail.load">刷新</n-button>
      </template>
    </page-header>
    <n-alert v-if="detail.error.value" type="error">{{ detail.error.value }}</n-alert>
    <n-spin v-if="!stack && detail.loading.value" size="small" />
    <template v-if="stack">
      <section class="stack-detail__summary">
        <div class="stack-detail__identity">
          <stack-state-tag :state="state" />
          <n-tag size="small" :bordered="false">{{ stack.managed ? '已纳管' : '外部项目' }}</n-tag>
          <span class="stack-detail__counts">{{ stack.running ?? '—' }} / {{ stack.total ?? '—' }} 个容器运行中</span>
        </div>
        <n-alert v-if="!isOnline" type="warning" :bordered="false">服务器离线，无法获取实时容器与日志，执行操作暂不可用。</n-alert>
        <n-alert v-if="managed?.drift" type="warning" :bordered="false" title="配置漂移">服务器上的 compose.yml 与 Hub 记录不一致。保存并部署会覆盖服务器配置，请先确认线上改动。</n-alert>
        <n-alert v-if="managed && managed.synced_at === 0" type="warning" :bordered="false" title="尚未同步到服务器">{{ managed.sync_hint || '配置已记录在 Hub，但尚未成功下发。请检查同步状态，在此期间不要启动或重建。' }}</n-alert>
        <n-alert v-if="!stack.managed" type="info" :bordered="false">外部项目的配置编辑只保存 Hub 草稿，不修改服务器文件、不启动或重启容器。登记与复制需要管理权限。</n-alert>
        <n-alert v-if="!stack.managed && stack.auto_adopt_reason" type="warning" :bordered="false">{{ stack.auto_adopt_reason }}</n-alert>
        <managed-stack-actions v-if="managed" :key="projectKey" :agent-id="agentId ?? ''" :stack-name="stackName" :online="isOnline" :busy="state === 'busy'" :unsynced="managed.synced_at === 0" @changed="detail.load" />
      </section>
      <section class="stack-detail__content">
        <n-tabs v-model:value="activeTab" type="line">
          <n-tab-pane name="containers" tab="容器">
            <n-alert v-if="detail.containersError.value" type="warning">{{ detail.containersError.value }}</n-alert>
            <stack-containers-table v-else :containers="detail.containers.value" :agent-id="agentId ?? ''" :direct-address="agent?.direct_address ?? ''" />
          </n-tab-pane>
          <n-tab-pane name="config" tab="配置" display-directive="show:lazy">
            <managed-stack-config-panel v-if="managed" :key="projectKey + ':managed'" :agent-id="agentId ?? ''" :stack-name="stackName" @changed="detail.load" />
            <external-stack-config-section v-else :key="projectKey + ':external'" :agent-id="agentId ?? ''" :stack-name="stackName" @changed="detail.load" />
          </n-tab-pane>
          <n-tab-pane name="logs" tab="日志">
            <stack-container-logs v-if="activeTab === 'logs'" :key="projectKey" :agent-id="agentId ?? ''" :containers="detail.containers.value" :online="isOnline" />
          </n-tab-pane>
        </n-tabs>
      </section>
    </template>
  </div>
</template>

<style scoped>
.stack-detail { display: flex; flex-direction: column; gap: 16px; }
.stack-detail__summary, .stack-detail__content { background: var(--arkade-card-bg, #fff); border: 1px solid var(--arkade-border-color); border-radius: 8px; padding: 16px; }
.stack-detail__summary { display: flex; flex-direction: column; gap: 12px; }
.stack-detail__identity { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.stack-detail__counts { font-size: 13px; color: var(--arkade-text-tertiary); }
@media (max-width: 767.98px) { .stack-detail__summary, .stack-detail__content { padding: 12px; } }
</style>
