<script setup lang="ts">
/**
 * 网络列表页 /agents/:id/networks
 *
 * 功能：列出网络（名称/驱动/范围/接入容器数/标签），支持多选批量删除与一键清理。
 *
 * 三个关键约束：
 * 1. **Docker 预置网络**（bridge / host / none）删不掉也不该删，直接不给勾选；
 * 2. **有容器接入**的网络删除会被拒绝，同样提前排除；
 * 3. 清理只按规则扫「无容器接入且非内置」的网络——这两条都由后端数据判定，
 *    前端不另算一份，否则规则一改就漏。
 */
import { computed, h, onMounted, ref, watch } from 'vue'
import { NButton, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import AgentLabelList from '@/components/agent/AgentLabelList.vue'
import ResourcePage from '@/components/resource/ResourcePage.vue'
import { deleteNetwork, listNetworks } from '@/api/resources'
import { describeTargets, useBulkActions } from '@/composables/useBulkActions'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { clampPage, slicePage } from '@/composables/useLocalPager'
import { useResourceList } from '@/composables/useResourceList'
import type { NetworkInfo } from '@/types/resource'
import { confirmAction } from '@/utils/confirm'
import { toastError } from '@/utils/feedback'

const { agentId, isOnline } = useCurrentAgent()

/** Docker 内置网络名：这些是运行时自带的，删除必然失败 */
const BUILTIN_NETWORKS = new Set(['bridge', 'host', 'none'])

const { items, loading, errorText, loaded, refresh } = useResourceList<NetworkInfo>({
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
 * 多选与批量删除。
 *
 * 行键用网络 ID（与表格 row-key 一致）：网络名可能重名，
 * 只有 ID 是稳定身份。
 */
const bulk = useBulkActions<NetworkInfo>({
  items,
  keyOf: (item) => item.id,
  labelOf: (item) => item.name,
  removeOne: async (item) => {
    if (agentId.value === null) return
    await deleteNetwork(agentId.value, item.id)
  },
  refresh,
  unit: '个网络'
})

/**
 * 分页：每页 20 个（TABLE_PAGE_SIZE）。
 *
 * 只影响渲染哪一页——请求、统计口径仍基于全量 items。
 * 删掉最后一页仅剩的条目后页码可能越界，watch 负责夹回合法页。
 */
const page = ref(1)
const pagedItems = computed(() => slicePage(items.value, page.value))

watch(
  () => items.value.length,
  (count) => {
    page.value = clampPage(page.value, count)
  }
)

/**
 * 切换服务器时回到第 1 页：换了一台机器就是另一批网络，
 * 沿用旧页码会出现「凭空第 4 页」。同时清掉上一台机器的勾选，
 * 否则会拿旧 ID 去删新机器上的网络（删不到，但提示会莫名其妙）。
 */
watch(agentId, () => {
  page.value = 1
  bulk.clear()
})

/**
 * 能否删除某个网络。
 *
 * 后端也有同样的判断，但前端提前排除可以避免用户点了才看到失败；
 * 同时也把「为什么不能删」直接显示在界面上（见列里的禁用提示）。
 */
function isDeletable(row: NetworkInfo): boolean {
  const builtin = row.builtin === true || BUILTIN_NETWORKS.has(row.name)
  return !builtin && row.containers === 0
}

/** 当前页里可勾选的网络 ID：不可删的行不给勾选 */
const selectableOnPage = computed(() => pagedItems.value.filter(isDeletable).map((n) => n.id))

/** 已选中的网络（只含可删的） */
const selectedNetworks = computed(() =>
  items.value.filter((n) => bulk.checkedKeys.value.includes(n.id) && isDeletable(n))
)

/** 勾了却删不掉的数量（列表刚刷新、网络被新容器接入） */
const skippedCount = computed(() => bulk.checkedKeys.value.length - selectedNetworks.value.length)

/** 可清理的网络：非内置且无容器接入 */
const cleanableNetworks = computed(() => items.value.filter(isDeletable))

const subtitle = computed(() => {
  if (!loaded.value) return '正在加载网络…'
  const total = items.value.length
  const inUse = items.value.filter((n) => n.containers > 0).length
  const inUseText = inUse > 0 ? `，有容器接入的 ${inUse} 个` : ''
  return `共 ${total} 个网络${inUseText}`
})

/**
 * 确认要删的网络。
 *
 * 网络删除不影响数据，属于可恢复（重新创建即可），
 * 但会影响接入容器的网络连通性，因此仍做二次确认。
 */
async function confirmDelete(labels: string[]): Promise<boolean> {
  if (labels.length === 0) return false
  const single = labels.length === 1
  return confirmAction({
    title: single ? '确认删除网络？' : `确认删除 ${String(labels.length)} 个网络？`,
    content:
      (single ? `将删除网络「${labels[0]}」。` : `将删除以下网络：\n${describeTargets(labels)}`) +
      '\n\n删除后，依赖这些网络通信的容器将失去网络连通性。' +
      '如需要可重新创建同名网络，但容器需重新接入。',
    positiveText: single ? '删除' : `删除 ${String(labels.length)} 个`,
    negativeText: '取消',
    danger: true
  })
}

/** 批量删除已勾选的网络 */
async function handleBulkDelete(): Promise<void> {
  const targets = selectedNetworks.value
  if (targets.length === 0) return
  if (!(await confirmDelete(targets.map((n) => n.name)))) return
  await bulk.removeMany(
    targets.map((n) => ({ key: n.id, label: n.name })),
    skippedCount.value
  )
}

/**
 * 清理无用网络。
 *
 * 只删「非内置且无容器接入」的网络：内置的删不掉，
 * 有容器接入的删了会断掉正在跑的业务，都不在清理范围内。
 */
async function handleCleanup(): Promise<void> {
  const candidates = cleanableNetworks.value
  if (candidates.length === 0) {
    toastError('没有可清理的网络')
    return
  }
  const labels = candidates.map((n) => n.name)
  const confirmed = await confirmAction({
    title: `清理无用网络（${String(labels.length)} 个）`,
    content:
      `以下网络没有容器接入，且不是 Docker 内置网络，将被删除：\n${describeTargets(labels)}\n\n` +
      '这会移除它们的网络配置（子网、网关等）。有容器接入的网络与内置网络不会被清理。' +
      '若某个网络是给暂时停掉的容器预留的，请先不要清理。',
    positiveText: `删除 ${String(labels.length)} 个`,
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return
  await bulk.cleanup(candidates.map((n) => ({ key: n.id, label: n.name })))
}

/** 表头全选：只作用于当前页（跨页累积选择容易误删） */
function togglePageSelection(): void {
  bulk.togglePage(selectableOnPage.value)
}

/**
 * 不能删的原因：直接写在行的勾选提示里，而不是给一个干瘪的 disabled。
 *
 * 选中态是唯一入口，所以原因必须能在这里查到；否则用户只会看到
 * 这一行勾不上，却不知道为什么——而「为什么」恰恰是他最想知道的。
 */
function deleteDisabledReason(row: NetworkInfo): string {
  if (row.builtin === true || BUILTIN_NETWORKS.has(row.name)) return 'Docker 内置网络，不可删除'
  if (row.containers > 0) return `有 ${row.containers} 个容器接入，请先断开`
  return ''
}

const columns = computed<DataTableColumns<NetworkInfo>>(() => [
  {
    type: 'selection',
    width: 40,
    // 内置网络与有容器接入的网络不给勾选：让「能勾的都能删」成为可依赖的直觉
    disabled: (row) => !isDeletable(row)
  },
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
    minWidth: 120,
    render: (row) => {
      const tag = h(
        NTag,
        { size: 'small', bordered: false, type: row.containers > 0 ? 'warning' : 'default' },
        { default: () => `${row.containers} 个` }
      )
      // 不能删时把原因挂成 title：这是用户看到勾选灰掉后唯一的解释入口
      const reason = deleteDisabledReason(row)
      return reason === '' ? tag : h('span', { title: reason }, [tag])
    }
  },
  {
    title: '标签',
    key: 'labels',
    minWidth: 160,
    render: (row) => h(AgentLabelList, { labels: row.labels })
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
    :data="pagedItems"
    :loading="loading"
    :loaded="loaded"
    :error-text="errorText"
    :online="isOnline"
    :page="page"
    :item-count="items.length"
    unit="个"
    empty-description="这台服务器上还没有自定义网络。编排创建的网络会出现在这里。"
    :scroll-x="880"
    :row-key="(row: NetworkInfo) => row.id"
    :checked-keys="bulk.checkedKeys.value"
    @update:page="(value: number) => (page = value)"
    @update:checked-keys="(keys: string[]) => (bulk.checkedKeys.value = keys)"
    @refresh="refresh"
  >
    <template #actions>
      <!--
        清理：按规则扫全量（非内置且无容器接入），不看当前页与当前勾选。
      -->
      <n-button
        size="small"
        :disabled="!isOnline || cleanableNetworks.length === 0 || bulk.busy.value"
        :loading="bulk.busy.value && bulk.checkedKeys.value.length === 0"
        title="删除所有没有容器接入、且非 Docker 内置的网络"
        @click="void handleCleanup()"
      >
        清理无用（{{ cleanableNetworks.length }}）
      </n-button>
      <n-button
        size="small"
        type="error"
        secondary
        :disabled="selectedNetworks.length === 0 || bulk.busy.value"
        :loading="bulk.busy.value && bulk.checkedKeys.value.length > 0"
        @click="void handleBulkDelete()"
      >
        删除{{ selectedNetworks.length > 0 ? `（${selectedNetworks.length}）` : '' }}
      </n-button>
      <n-button
        v-if="selectableOnPage.length > 0"
        size="small"
        quaternary
        :disabled="bulk.busy.value"
        @click="togglePageSelection"
      >
        全选本页
      </n-button>
    </template>
  </resource-page>
</template>
