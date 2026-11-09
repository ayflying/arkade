<script setup lang="ts">
/**
 * 网络列表页 /agents/:id/networks
 *
 * 功能：列出网络（名称/驱动/范围/接入容器数/标签），支持删除。
 *
 * 两个关键约束：
 * 1. **Docker 预置网络**（bridge / host / none）删不掉也不该删，
 *    直接禁用删除按钮，不让用户白点；
 * 2. **有容器接入**的网络删除会被拒绝，同样提前禁用并说明原因。
 */
import { computed, h, onMounted } from 'vue'
import { NButton, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import AgentLabelList from '@/components/agent/AgentLabelList.vue'
import ResourcePage from '@/components/resource/ResourcePage.vue'
import { deleteNetwork, listNetworks } from '@/api/resources'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { useResourceList } from '@/composables/useResourceList'
import type { NetworkInfo } from '@/types/resource'
import { confirmAction } from '@/utils/confirm'

const { agentId, isOnline } = useCurrentAgent()

/** Docker 内置网络名：这些是运行时自带的，删除必然失败 */
const BUILTIN_NETWORKS = new Set(['bridge', 'host', 'none'])

const { items, loading, errorText, loaded, busyKey, refresh, runDelete } =
  useResourceList<NetworkInfo>({
    load: async () => {
      if (agentId.value === null) return []
      const res = await listNetworks(agentId.value)
      return res.networks
    },
    remove: async (item) => {
      if (agentId.value === null) return
      await deleteNetwork(agentId.value, item.id)
    },
    loadErrorMessage: '加载网络列表失败',
    deleteSuccessMessage: '网络已删除'
  })

/**
 * 能否删除某个网络。
 *
 * 后端也有同样的判断，但前端提前禁用可以避免用户点了才看到失败；
 * 同时也把「为什么不能删」直接显示在界面上（见列里的禁用提示）。
 */
function isDeletable(row: NetworkInfo): boolean {
  const builtin = row.builtin === true || BUILTIN_NETWORKS.has(row.name)
  return !builtin && row.containers === 0
}

const subtitle = computed(() => {
  if (!loaded.value) return '正在加载网络…'
  const total = items.value.length
  const inUse = items.value.filter((n) => n.containers > 0).length
  const inUseText = inUse > 0 ? `，有容器接入的 ${inUse} 个` : ''
  return `共 ${total} 个网络${inUseText}`
})

/**
 * 删除网络。
 *
 * 网络删除不影响数据，属于可恢复（重新创建即可），
 * 但会影响接入容器的网络连通性，因此仍做二次确认。
 */
async function handleDelete(row: NetworkInfo): Promise<void> {
  if (!isDeletable(row)) return

  const confirmed = await confirmAction({
    title: '确认删除网络？',
    content:
      `将删除网络「${row.name}」（驱动：${row.driver}）。\n\n` +
      '删除后，依赖该网络通信的容器将失去网络连通性。' +
      '如需要可重新创建同名网络，但容器需重新接入。',
    positiveText: '删除',
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return

  await runDelete(row, row.id)
}

/** 删除按钮的禁用原因：给出具体原因而不是干瘪的 disabled */
function deleteDisabledReason(row: NetworkInfo): string {
  if (row.builtin === true || BUILTIN_NETWORKS.has(row.name)) return 'Docker 内置网络，不可删除'
  if (row.containers > 0) return `有 ${row.containers} 个容器接入，请先断开`
  return ''
}

const columns = computed<DataTableColumns<NetworkInfo>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 180,
    render: (row) => {
      const tagNodes = [h('span', { class: 'arkade-cell-strong' }, row.name)]
      if (row.builtin === true || BUILTIN_NETWORKS.has(row.name)) {
        tagNodes.push(
          h(
            NTag,
            { size: 'tiny', bordered: false, style: 'margin-left:6px' },
            { default: () => '内置' }
          )
        )
      }
      return h('span', {}, tagNodes)
    }
  },
  {
    title: '驱动',
    key: 'driver',
    minWidth: 100
  },
  {
    title: '范围',
    key: 'scope',
    minWidth: 90
  },
  {
    title: '接入容器',
    key: 'containers',
    minWidth: 100,
    render: (row) =>
      h(
        NTag,
        { size: 'small', bordered: false, type: row.containers > 0 ? 'warning' : 'default' },
        { default: () => `${row.containers} 个` }
      )
  },
  {
    title: '标签',
    key: 'labels',
    minWidth: 160,
    render: (row) => h(AgentLabelList, { labels: row.labels })
  },
  {
    title: '操作',
    key: 'actions',
    width: 110,
    fixed: 'right',
    render: (row) => {
      const reason = deleteDisabledReason(row)
      const button = h(
        NButton,
        {
          size: 'tiny',
          quaternary: true,
          type: 'error',
          disabled: reason !== '' || busyKey.value !== '',
          loading: busyKey.value === row.id,
          onClick: () => void handleDelete(row)
        },
        { default: () => '删除' }
      )
      // 禁用时加 title，鼠标悬停能看到「为什么不能删」
      return reason === '' ? button : h('span', { title: reason }, [button])
    }
  }
])

onMounted(() => {
  void refresh()
})
</script>

<template>
  <resource-page
    title="网络"
    :subtitle="subtitle"
    :columns="columns"
    :data="items"
    :loading="loading"
    :loaded="loaded"
    :error-text="errorText"
    :online="isOnline"
    empty-description="这台服务器上还没有自定义网络。编排创建的网络会出现在这里。"
    :scroll-x="950"
    :row-key="(row: NetworkInfo) => row.id"
    @refresh="refresh"
  />
</template>
