<script setup lang="ts">
/**
 * 容器挂载点表
 *
 * 挂载会暴露宿主路径，属于 manage 权限范围，所以无权限时不渲染表格
 * 而是给一句说明——渲染一张空表会让人以为「这个容器没有挂载」，
 * 那是个错误的结论。
 */
import { computed, h } from 'vue'
import { NDataTable, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import type { ContainerMount } from '@/types/container'

interface Props {
  mounts: ContainerMount[]
  forbidden: boolean
}

const props = defineProps<Props>()

const columns = computed<DataTableColumns<ContainerMount>>(() => [
  {
    title: '类型',
    key: 'type',
    width: 100,
    render: (row) => h(NTag, { size: 'small', bordered: false }, { default: () => row.type || '—' })
  },
  {
    title: '宿主来源',
    key: 'source',
    minWidth: 220,
    ellipsis: { tooltip: true },
    render: (row) => h('span', { class: 'arkade-cell-mono' }, row.source || '—')
  },
  {
    title: '容器内路径',
    key: 'destination',
    minWidth: 220,
    ellipsis: { tooltip: true },
    render: (row) => h('span', { class: 'arkade-cell-mono' }, row.destination || '—')
  },
  {
    title: '读写',
    key: 'read_only',
    width: 90,
    render: (row) => (row.read_only ? '只读' : '可写')
  }
])
</script>

<template>
  <div v-if="forbidden" class="mounts__empty">查看挂载需要该服务器的管理权限。</div>
  <div v-else-if="mounts.length === 0" class="mounts__empty">该容器没有挂载任何目录或卷。</div>
  <n-data-table
    v-else
    :columns="columns"
    :data="mounts"
    :bordered="false"
    :single-line="false"
    size="small"
    :row-key="(row: ContainerMount) => `${row.type}:${row.source}:${row.destination}`"
    :scroll-x="700"
  />
</template>

<style scoped>
.mounts__empty {
  padding: 16px 0;
  font-size: 13px;
  color: var(--arkade-text-tertiary);
}
</style>
