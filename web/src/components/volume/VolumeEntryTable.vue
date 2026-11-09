<script setup lang="ts">
/**
 * 卷内目录表格
 *
 * 只负责「展示 + 抛出意图」，具体操作（进入/打开/重命名/删除）由父组件处理。
 * 这样表格本身没有网络调用，容易改也容易测。
 *
 * ## 按类型区分可做的操作
 *
 * - `directory`：可进入、可重命名、可删除（后端只允许删空目录）；
 * - `file`：可打开、可重命名、可删除；
 * - `symlink` / `unsupported`：**只展示**。符号链接可能指向卷外，
 *   跟进去读等于绕过卷边界；设备文件/socket 没有「内容」这个概念。
 *   界面明确说明为什么不能操作，而不是给个灰按钮让用户猜。
 */
import { computed, h } from 'vue'
// NDataTable 必须显式引入：项目没有全局注册 Naive 组件，
// 而模板里的 <n-data-table> 是 kebab 形式，vue-tsc 对未注册标签**不报错**，
// 于是漏了它整张表格会静默不渲染（页面有数据却没有任何行）。
import { NButton, NDataTable, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import type { VolumeEntry, VolumeEntryKind } from '@/types/volumeFile'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'
import { formatBytes } from '@/utils/formatSize'

interface Props {
  entries: VolumeEntry[]
  loading: boolean
  /** 名称过滤关键词 */
  keyword: string
  /** 正在操作中的条目名，用于按钮 loading */
  busyName?: string
}

const props = withDefaults(defineProps<Props>(), { busyName: '' })

const emit = defineEmits<{
  open: [entry: VolumeEntry]
  rename: [entry: VolumeEntry]
  remove: [entry: VolumeEntry]
}>()

/** 类型 → 中文标签 + 配色 */
const KIND_META: Record<VolumeEntryKind, { label: string; type: 'default' | 'info' | 'warning' }> = {
  directory: { label: '目录', type: 'info' },
  file: { label: '文件', type: 'default' },
  symlink: { label: '符号链接', type: 'warning' },
  unsupported: { label: '不支持', type: 'warning' }
}

/** 该类型是否允许任何写操作 */
function canModify(kind: VolumeEntryKind): boolean {
  return kind === 'directory' || kind === 'file'
}

/** 名称过滤：不区分大小写；目录在前再按名称排序在父组件已处理 */
const filtered = computed<VolumeEntry[]>(() => {
  const kw = props.keyword.trim().toLowerCase()
  if (kw === '') return props.entries
  return props.entries.filter((e) => e.name.toLowerCase().includes(kw))
})

const columns = computed<DataTableColumns<VolumeEntry>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 260,
    render: (row) => {
      const isDir = row.kind === 'directory'
      const canOpen = isDir || row.kind === 'file'
      const label = h(
        'span',
        {
          class: [
            'entry-name',
            isDir ? 'entry-name--dir' : '',
            canOpen ? 'entry-name--clickable' : ''
          ]
            .filter((s) => s !== '')
            .join(' '),
          title: row.name
        },
        row.name
      )
      if (!canOpen) {
        // 不可打开的类型渲染成纯文本：不给出可点击的假象
        return label
      }
      return h(
        'button',
        {
          type: 'button',
          class: 'entry-name-btn',
          onClick: () => emit('open', row)
        },
        [label]
      )
    }
  },
  {
    title: '类型',
    key: 'kind',
    width: 110,
    render: (row) => {
      const meta = KIND_META[row.kind] ?? KIND_META.unsupported
      return h(NTag, { size: 'small', bordered: false, type: meta.type }, { default: () => meta.label })
    }
  },
  {
    title: '大小',
    key: 'size',
    width: 100,
    // 目录大小无意义，显示「—」而不是 0 B
    render: (row) => (row.kind === 'directory' ? '—' : formatBytes(row.size))
  },
  {
    title: '权限',
    key: 'mode',
    width: 110,
    render: (row) => h('span', { class: 'arkade-cell-mono' }, row.mode === '' ? '—' : row.mode)
  },
  {
    title: '修改时间',
    key: 'modified_at',
    width: 150,
    render: (row) =>
      row.modified_at === 0
        ? '—'
        : h('span', { title: formatAbsoluteTime(row.modified_at) }, formatRelativeTime(row.modified_at))
  },
  {
    title: '操作',
    key: 'actions',
    width: 150,
    fixed: 'right',
    render: (row) => {
      // symlink / unsupported：只展示，不给任何可点入口
      if (!canModify(row.kind)) {
        return h('span', { class: 'entry-note' }, '只读展示')
      }
      const busy = props.busyName !== ''
      return h('div', { class: 'arkade-row-actions' }, [
        h(
          NButton,
          {
            size: 'tiny',
            quaternary: true,
            disabled: busy,
            onClick: () => emit('rename', row)
          },
          { default: () => '重命名' }
        ),
        h(
          NButton,
          {
            size: 'tiny',
            quaternary: true,
            type: 'error',
            disabled: busy,
            loading: props.busyName === row.name,
            onClick: () => emit('remove', row)
          },
          { default: () => '删除' }
        )
      ])
    }
  }
])
</script>

<template>
  <div class="entry-table">
    <n-data-table
      :columns="columns"
      :data="filtered"
      :loading="loading"
      :row-key="(row: VolumeEntry) => row.name"
      :scroll-x="900"
      :bordered="false"
      size="small"
    />
    <p v-if="!loading && filtered.length === 0" class="entry-table__empty">
      {{ keyword.trim() === '' ? '这个目录是空的。' : `没有名称包含「${keyword.trim()}」的条目。` }}
    </p>
  </div>
</template>

<style scoped>
.entry-table {
  margin-top: 12px;
}

.entry-table__empty {
  margin: 16px 0 0;
  font-size: 13px;
  color: var(--arkade-text-tertiary);
  text-align: center;
}

.entry-name {
  font-size: 13px;
}

.entry-name--dir {
  font-weight: 600;
}

.entry-name--clickable {
  color: var(--arkade-link-color, #2080f0);
}

/* 用真正的 button 承载点击：键盘可达、有焦点样式，不是 div 假装可点 */
.entry-name-btn {
  padding: 0;
  border: none;
  background: none;
  cursor: pointer;
  text-align: left;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font: inherit;
}

.entry-name-btn:hover .entry-name {
  text-decoration: underline;
}

.entry-name-btn:focus-visible {
  outline: 2px solid var(--arkade-link-color, #2080f0);
  outline-offset: 2px;
  border-radius: 2px;
}

.entry-note {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}
</style>
