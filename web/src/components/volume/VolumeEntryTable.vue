<script setup lang="ts">
import { computed, h } from 'vue'
import { NButton, NDataTable, NDropdown, NEmpty } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import type { VolumeEntry } from '@/types/volumeFile'
import { VOLUME_FILE_MAX_BYTES } from '@/types/volumeFile'
import { formatAbsoluteTime } from '@/utils/formatTime'
import { formatBytes } from '@/utils/formatSize'

const props = defineProps<{
  entries: VolumeEntry[]
  loading: boolean
  disabled: boolean
  busyName: string
  downloadingName: string
}>()

const emit = defineEmits<{
  open: [entry: VolumeEntry]
  download: [entry: VolumeEntry]
  rename: [entry: VolumeEntry]
  remove: [entry: VolumeEntry]
}>()

function rowAction(action: string, entry: VolumeEntry): void {
  if (props.disabled || props.loading || props.busyName !== '') return
  if (action === 'download' && props.downloadingName === '' && entry.size <= VOLUME_FILE_MAX_BYTES) {
    emit('download', entry)
  } else if (action === 'rename') {
    emit('rename', entry)
  } else if (action === 'remove') {
    emit('remove', entry)
  }
}

const columns = computed<DataTableColumns<VolumeEntry>>(() => [
  {
    title: '名称',
    key: 'name',
    ellipsis: true,
    render: (row) => {
      const isDir = row.kind === 'directory'
      if (!isDir && row.kind !== 'file') {
        return h('span', { title: row.name }, `${row.name}（${row.kind === 'symlink' ? '符号链接' : '特殊文件'}，仅展示）`)
      }
      return h('button', {
        type: 'button',
        class: ['entry-name', { 'entry-name--directory': isDir }],
        title: row.name,
        disabled: props.disabled || props.loading,
        onClick: () => emit('open', row)
      }, [
        ...(isDir ? [h('svg', {
          viewBox: '0 0 24 24', width: 16, height: 16, fill: 'none', stroke: 'currentColor',
          'stroke-width': 1.6, 'aria-hidden': 'true'
        }, [h('path', { d: 'M3 6h7l2 2h9v11H3z', 'stroke-linejoin': 'round' })])] : []),
        h('span', row.name)
      ])
    }
  },
  {
    title: '大小',
    key: 'size',
    width: 90,
    render: (row) => row.kind === 'file' ? formatBytes(row.size) : '—'
  },
  {
    title: '修改时间',
    key: 'modified_at',
    width: 160,
    render: (row) => row.modified_at === 0 ? '—' : formatAbsoluteTime(row.modified_at)
  },
  {
    title: '操作',
    key: 'actions',
    width: 60,
    align: 'center',
    render: (row) => {
      if (row.kind !== 'directory' && row.kind !== 'file') return '—'
      const disabled = props.disabled || props.loading || props.busyName !== ''
      const options = [
        ...(row.kind === 'file' ? [{
          label: row.size > VOLUME_FILE_MAX_BYTES ? '下载（超出 4 MiB）' : '下载',
          key: 'download',
          disabled: props.downloadingName !== '' || row.size > VOLUME_FILE_MAX_BYTES
        }] : []),
        { label: '重命名', key: 'rename' },
        { label: '删除', key: 'remove' }
      ]
      return h(NDropdown, { options, disabled, trigger: 'click', onSelect: (key: string) => rowAction(key, row) }, {
        default: () => h(NButton, {
          size: 'small', quaternary: true, disabled,
          loading: props.busyName === row.name || props.downloadingName === row.name,
          'aria-label': `${row.name}的操作`
        }, { default: () => '⋯' })
      })
    }
  }
])
</script>

<template>
  <n-data-table
    class="entry-table arkade-table-scroll"
    :columns="columns"
    :data="entries"
    :loading="loading"
    :row-key="(row: VolumeEntry) => row.name"
    :scroll-x="480"
    :bordered="false"
    size="small"
  >
    <template #empty>
      <n-empty description="这个目录是空的" />
    </template>
  </n-data-table>
</template>

<style scoped>
.entry-table { margin-top: 12px; }

.entry-table :deep(.entry-name) {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
  padding: 2px 0;
  border: 0;
  background: none;
  color: var(--arkade-link-color, var(--arkade-primary));
  font: inherit;
  cursor: pointer;
  text-align: left;
}

.entry-table :deep(.entry-name--directory) { font-weight: 600; }
.entry-table :deep(.entry-name svg) { flex: none; }
.entry-table :deep(.entry-name span) { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.entry-table :deep(.entry-name:hover:not(:disabled)) { text-decoration: underline; }
.entry-table :deep(.entry-name:focus-visible) { outline: 2px solid currentColor; outline-offset: 2px; }
.entry-table :deep(.entry-name:disabled) { color: var(--arkade-text-secondary); cursor: default; }
</style>
