<script setup lang="ts">
/**
 * 统一的编排表格
 *
 * 已纳管与未纳管合并成一张表（合并规则见 utils/stackListModel）。
 *
 * ## 设计取向：信息密度优先
 *
 * 这是运维每天扫一遍的表，不是展示页：
 * - 不做卡片、不做大图标，行高紧凑，一屏能看二十个；
 * - 颜色只用于**状态**，不用来做装饰，配色走项目现有 tokens；
 * - 长文本一律截断 + tooltip，不在行内换行把表格撑高。
 *
 * ## 一列说明一件事
 *
 * 「运行/总容器」把两个数字放在同一列，因为用户看的是比例（3/3 全绿 3/0 全红），
 * 拆成两列反而要横向对照。`null` 显示「—」而不是 0——未知和零是两回事。
 */
import { computed, h } from 'vue'
import { NDataTable, NTag, NTooltip } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import StackStateTag from '@/components/stack/StackStateTag.vue'
import type { StackListRow } from '@/utils/stackListModel'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'

interface Props {
  rows: StackListRow[]
  loading?: boolean
}

const props = withDefaults(defineProps<Props>(), { loading: false })

const emit = defineEmits<{
  /** 查看详情：所有行都进同一个详情页 */
  (e: 'open', name: string): void
}>()

/**
 * 登记状态标签。
 *
 * 三态而不是两态：`unsynced`（Arkade 有记录但没下发到服务器）
 * 和 `unregistered`（Arkade 里根本没记录）是完全不同的处境，
 * 合并成一个「未同步」会让用户不知道该去服务器还是该来 Arkade。
 */
const REG_META: Record<StackListRow['regState'], { label: string; type: 'success' | 'info' | 'default' }> = {
  synced: { label: '已同步', type: 'success' },
  unsynced: { label: '未同步', type: 'info' },
  unregistered: { label: '未登记', type: 'default' }
}

/** 配置来源文案：直接说「改配置去哪里」，不说实现细节 */
function sourceLabel(row: StackListRow): string {
  return row.source === 'arkade' ? 'Arkade' : '服务器原目录'
}

/** 运行/总容器：null 表示未知，显示「—」 */
function containerText(row: StackListRow): string {
  if (row.running === null && row.total === null) return '—'
  const running = row.running === null ? '—' : String(row.running)
  const total = row.total === null ? '—' : String(row.total)
  return `${running}/${total}`
}

/** 运行数颜色：全部在跑用绿、一个没跑用灰、部分用橙 */
function runningType(row: StackListRow): 'success' | 'warning' | 'default' {
  if (typeof row.running !== 'number' || typeof row.total !== 'number') return 'default'
  if (row.total === 0) return 'default'
  if (row.running === row.total) return 'success'
  if (row.running === 0) return 'default'
  return 'warning'
}

const columns = computed<DataTableColumns<StackListRow>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 200,
    ellipsis: { tooltip: true },
    render: (row) =>
      h(
        'a',
        { class: 'arkade-link arkade-cell-mono', onClick: () => emit('open', row.name) },
        row.name
      )
  },
  {
    title: '状态',
    key: 'status',
    width: 130,
    render: (row) => h(StackStateTag, { state: row.status })
  },
  {
    title: '容器',
    key: 'containers',
    width: 90,
    render: (row) =>
      h(
        NTag,
        { size: 'small', bordered: false, type: runningType(row) },
        { default: () => containerText(row) }
      )
  },
  {
    title: '配置来源',
    key: 'source',
    width: 130,
    render: (row) => sourceLabel(row)
  },
  {
    title: '登记状态',
    key: 'regState',
    width: 120,
    render: (row) => {
      const meta = REG_META[row.regState]
      const tag = h(
        NTag,
        { size: 'small', bordered: false, type: meta.type },
        { default: () => meta.label }
      )
      // 原因用 tooltip 补充；没有原因时就不挂 tooltip，避免空悬浮框
      if (row.regReason === '') return tag
      return h(
        NTooltip,
        { trigger: 'hover' },
        {
          trigger: () => tag,
          default: () => h('div', { class: 'stack-unified__tip' }, row.regReason)
        }
      )
    }
  },
  {
    title: '更新时间',
    key: 'updatedAt',
    width: 130,
    render: (row) => {
      // 外部项目拿不到更新时间，显示「—」而不是「1970」
      if (row.updatedAt === 0) return '—'
      return h(
        'span',
        { title: formatAbsoluteTime(row.updatedAt) },
        formatRelativeTime(row.updatedAt)
      )
    }
  },
  {
    title: '操作',
    key: 'actions',
    width: 90,
    render: (row) =>
      h(
        'a',
        { class: 'arkade-link', onClick: () => emit('open', row.name) },
        '查看详情'
      )
  }
])

/** 空表说明：两处来源都为空时，用户需要知道「这里本该有什么」 */
const EMPTY_TEXT = '这台服务器上还没有可显示的编排'
</script>

<template>
  <n-data-table
    :columns="columns"
    :data="props.rows"
    :loading="props.loading"
    :bordered="false"
    :single-line="false"
    size="small"
    :row-key="(row: StackListRow) => row.key"
    :scroll-x="900"
    :locale="{ emptyText: EMPTY_TEXT }"
  />
</template>

<style scoped>
/* tooltip 里的原因可能是整句中文，给它一个舒适的换行宽度 */
.stack-unified__tip {
  max-width: 260px;
  line-height: 1.5;
}
</style>
