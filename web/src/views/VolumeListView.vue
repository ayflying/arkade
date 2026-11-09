<script setup lang="ts">
/**
 * 存储卷列表页 /agents/:id/volumes
 *
 * 功能：列出卷（名称/驱动/挂载点/是否使用中/标签），支持删除。
 *
 * 删除卷是这个应用里**最危险**的操作之一：
 * 卷里装的是容器数据（数据库文件、上传文件等），删掉即永久丢失且无法恢复。
 * 因此确认文案必须把「不可恢复」说透，并且对「使用中」的卷直接禁用删除按钮
 * （后端也会拒绝，但提前禁用避免用户白点一次）。
 */
import { computed, h, onMounted } from 'vue'
import { NButton, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import AgentLabelList from '@/components/agent/AgentLabelList.vue'
import ResourcePage from '@/components/resource/ResourcePage.vue'
import { deleteVolume, listVolumes } from '@/api/resources'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { useResourceList } from '@/composables/useResourceList'
import type { VolumeInfo } from '@/types/resource'
import { confirmAction } from '@/utils/confirm'

const { agentId, isOnline } = useCurrentAgent()

const { items, loading, errorText, loaded, busyKey, refresh, runDelete } = useResourceList<VolumeInfo>({
  load: async () => {
    if (agentId.value === null) return []
    const res = await listVolumes(agentId.value)
    return res.volumes
  },
  remove: async (item) => {
    if (agentId.value === null) return
    await deleteVolume(agentId.value, item.name)
  },
  loadErrorMessage: '加载存储卷列表失败',
  deleteSuccessMessage: '存储卷已删除'
})

const subtitle = computed(() => {
  if (!loaded.value) return '正在加载存储卷…'
  const total = items.value.length
  const inUse = items.value.filter((v) => v.in_use).length
  return `共 ${total} 个存储卷，使用中 ${inUse} 个`
})

/**
 * 删除单个卷。
 *
 * 二次确认文案刻意写重：
 * - 明确指出「卷内所有数据将被永久删除，无法恢复」；
 * - 带上卷名，避免误删相邻的同类卷；
 * - 提示备份先行。
 */
async function handleDelete(item: VolumeInfo): Promise<void> {
  if (item.in_use) return

  const confirmed = await confirmAction({
    title: '确认删除存储卷？',
    content:
      `将删除存储卷「${item.name}」。\n\n` +
      '卷内保存的所有数据（数据库文件、上传文件、日志等）会被永久删除，' +
      '此操作不可撤销、无法恢复。如果这些数据还有用，请先备份。',
    positiveText: '永久删除',
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return

  await runDelete(item, item.name)
}

const columns = computed<DataTableColumns<VolumeInfo>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 180,
    render: (row) => h('span', { class: 'arkade-cell-strong' }, row.name)
  },
  {
    title: '驱动',
    key: 'driver',
    minWidth: 100
  },
  {
    title: '挂载点',
    key: 'mountpoint',
    minWidth: 260,
    // 宿主机路径很长：等宽字体 + 省略号，完整路径放 title 提示
    render: (row) =>
      h('span', { class: 'arkade-cell-mono', title: row.mountpoint }, row.mountpoint)
  },
  {
    title: '使用状态',
    key: 'in_use',
    minWidth: 100,
    render: (row) =>
      h(
        NTag,
        { size: 'small', bordered: false, type: row.in_use ? 'warning' : 'default' },
        { default: () => (row.in_use ? '使用中' : '未使用') }
      )
  },
  {
    title: '标签',
    key: 'labels',
    minWidth: 180,
    render: (row) => h(AgentLabelList, { labels: row.labels })
  },
  {
    title: '操作',
    key: 'actions',
    width: 100,
    fixed: 'right',
    render: (row) =>
      h(
        NButton,
        {
          size: 'tiny',
          quaternary: true,
          type: 'error',
          // 使用中的卷无法删除：后端会拒绝，这里直接禁用
          disabled: row.in_use || busyKey.value !== '',
          loading: busyKey.value === row.name,
          onClick: () => void handleDelete(row)
        },
        { default: () => '删除' }
      )
  }
])

onMounted(() => {
  void refresh()
})
</script>

<template>
  <resource-page
    title="存储卷"
    :subtitle="subtitle"
    :columns="columns"
    :data="items"
    :loading="loading"
    :loaded="loaded"
    :error-text="errorText"
    :online="isOnline"
    empty-description="这台服务器上还没有任何存储卷。容器启动后声明的数据卷会出现在这里。"
    :scroll-x="1000"
    :row-key="(row: VolumeInfo) => row.name"
    @refresh="refresh"
  />
</template>
