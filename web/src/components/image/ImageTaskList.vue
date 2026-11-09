<script setup lang="ts">
/**
 * 镜像任务列表（页内、非全局任务中心）
 *
 * 展示当前服务器上 Hub 内存里的镜像任务：拉取 / 导出 / 导入 / 迁移。
 *
 * ## 必须讲清的四件事（写在界面上，不是只写注释）
 *
 * 1. **任务只存在 Hub 内存**：Hub 重启后列表清空，这不是失败，是设计如此；
 * 2. **导出链接 30 分钟过期**，且是走登录 Cookie 的普通链接，会重新校验权限；
 * 3. **取消不回滚**：引擎已写入的部分不撤回，import/migrate 可能已更新同名 tag；
 * 4. **进度未知时不假装有百分比**：progress 为 null 时显示「—」而不是 0%。
 */
import { computed, h, watch } from 'vue'
import { NButton, NProgress, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import type { ImageTask, ImageTaskKind, ImageTaskStatus } from '@/types/imageTask'
import { imageDownloadUrl } from '@/api/imageTasks'
import { formatBytes } from '@/utils/formatSize'
import { isTaskActive } from '@/composables/useImageTasks'

interface Props {
  tasks: ImageTask[]
  loading?: boolean
  /** 当前服务器 id，用于把 target_agent_id 显示成名字 */
  agentId: string
  /** 服务器 id → 显示名；拿不到就显示 id，不能显示空白 */
  agentNames?: Record<string, string>
  /** 取消中/删除中的任务 id，用于按钮 loading */
  cancellingId?: string
}

const props = withDefaults(defineProps<Props>(), {
  loading: false,
  agentNames: () => ({}),
  cancellingId: ''
})

const emit = defineEmits<{
  cancel: [task: ImageTask]
  refresh: []
  /**
   * 任务状态发生变化。
   *
   * 页面据此在任务结束时刷新镜像列表：
   * 拉取/导入/迁移成功后镜像集合会变，但任务进行中刷新没有意义
   * （镜像还没写完，刷了也看不到）。
   */
  taskSettled: [task: ImageTask]
}>()

/** 上一轮的结束状态快照：只在本轮从「未结束」变为「已结束」时通知一次 */
const settledSnapshot = new Map<string, boolean>()

/**
 * 监听任务完成跃迁。
 *
 * 不做这个判断的话，每次轮询刷新都会触发一次页面刷新——
 * 任务是 2 秒一轮，会导致镜像列表每 2 秒刷一次，白白打后端。
 */
watch(
  () => props.tasks,
  (list) => {
    for (const task of list) {
      const finished = !isTaskActive(task)
      const wasFinished = settledSnapshot.get(task.id)
      settledSnapshot.set(task.id, finished)
      // 只在「之前见过且当时未结束，现在结束了」时通知
      if (finished && wasFinished === false) {
        emit('taskSettled', task)
      }
    }
    // 清理已从列表消失的任务，避免 Map 无限增长
    const alive = new Set(list.map((t) => t.id))
    for (const id of settledSnapshot.keys()) {
      if (!alive.has(id)) settledSnapshot.delete(id)
    }
  },
  { deep: true }
)

/** 任务种类 → 中文名 */
const KIND_LABEL: Record<ImageTaskKind, string> = {
  pull: '拉取',
  export: '导出',
  import: '导入',
  migrate: '迁移'
}

/** 状态 → 文案 + 颜色（与项目其他状态映射保持同一套路） */
const STATUS_MAP: Record<ImageTaskStatus, { label: string; type: 'default' | 'info' | 'success' | 'warning' | 'error' }> = {
  pending: { label: '排队中', type: 'info' },
  running: { label: '进行中', type: 'info' },
  succeeded: { label: '已完成', type: 'success' },
  failed: { label: '失败', type: 'error' },
  canceled: { label: '已取消', type: 'default' }
}

/** 拿服务器显示名：优先名字，退化到 id，最后退化到「未知服务器」 */
function agentLabel(id: string | undefined): string {
  if (id === undefined || id === '') return '—'
  return props.agentNames[id] ?? id
}

/** 把 ISO 时间格式化成「MM-DD HH:mm:ss」；无效值原样返回，不抛错 */
function formatIsoTime(iso: string): string {
  if (iso === '') return '—'
  const date = new Date(iso)
  if (Number.isNaN(date.getTime())) return iso
  const pad = (n: number): string => String(n).padStart(2, '0')
  return `${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
}

/**
 * 进度显示。
 *
 * progress === null 表示**无法得知进度**（很多阶段算不出百分比）。
 * 这时不能显示 0%——0% 是一个具体结论（「一点都没动」），会误导用户以为卡住了。
 * 有字节数就显示字节（真实数据），否则显示「—」。
 */
function renderProgress(task: ImageTask) {
  if (task.status === 'succeeded') {
    return h('span', { class: 'arkade-cell-muted' }, '完成')
  }
  if (task.status === 'failed' || task.status === 'canceled') {
    return h('span', { class: 'arkade-cell-muted' }, '—')
  }

  const hasBytes = task.bytes_total !== null && task.bytes_total > 0
  const byteText = hasBytes
    ? `${formatBytes(task.bytes_done)} / ${formatBytes(task.bytes_total ?? 0)}`
    : task.bytes_done > 0
      ? formatBytes(task.bytes_done)
      : ''

  // 百分比来自后端真实计算；null 时绝不编造。
  // 走到这里 status 一定是 pending/running（上面已提前返回 succeeded/failed/canceled），
  // 所以不需要再判断失败态配色。
  if (task.progress !== null) {
    const percent = Math.min(100, Math.max(0, Math.round(task.progress * 100)))
    return h('div', { class: 'task-progress' }, [
      h(NProgress, {
        type: 'line',
        percentage: percent,
        height: 8,
        showIndicator: false
      }),
      h('span', { class: 'task-progress__text' }, byteText === '' ? `${percent}%` : `${percent}% · ${byteText}`)
    ])
  }

  /*
   * 进度未知：用不确定进度条 + 阶段文案。
   * 不确定进度条（indeterminate）在语义上就是「在做，但不知道还剩多少」。
   */
  return h('div', { class: 'task-progress' }, [
    h(NProgress, {
      type: 'line',
      percentage: 0,
      processing: true,
      height: 8,
      showIndicator: false
    }),
    h('span', { class: 'task-progress__text' }, byteText === '' ? '进度未知' : byteText)
  ])
}

const columns = computed<DataTableColumns<ImageTask>>(() => [
  {
    title: '操作',
    key: 'kind',
    width: 100,
    render: (row) => h('span', null, KIND_LABEL[row.kind] ?? row.kind)
  },
  {
    title: '状态',
    key: 'status',
    width: 90,
    render: (row) => {
      const meta = STATUS_MAP[row.status] ?? { label: row.status, type: 'default' as const }
      return h(NTag, { size: 'small', bordered: false, type: meta.type }, { default: () => meta.label })
    }
  },
  {
    title: '目标',
    key: 'target',
    width: 140,
    // 只有 migrate 有目标服务器；其余显示「—」而不是空白
    render: (row) =>
      row.kind === 'migrate'
        ? h('span', null, agentLabel(row.target_agent_id))
        : h('span', { class: 'arkade-cell-muted' }, '—')
  },
  {
    title: '进度 / 阶段',
    key: 'progress',
    minWidth: 240,
    render: (row) =>
      h('div', { class: 'task-cell' }, [
        renderProgress(row),
        h('div', { class: 'task-phase', title: row.phase }, row.phase === '' ? '—' : row.phase)
      ])
  },
  {
    title: '结果',
    key: 'message',
    minWidth: 200,
    // 失败原因/成功摘要都是后端给的中文，直接展示，不二次翻译
    render: (row) =>
      h(
        'span',
        { class: 'task-message', title: row.message },
        row.message === '' ? '—' : row.message
      )
  },
  {
    title: '创建时间',
    key: 'created_at',
    width: 150,
    render: (row) => h('span', { class: 'arkade-cell-muted' }, formatIsoTime(row.created_at))
  },
  {
    title: '操作',
    key: 'actions',
    width: 170,
    fixed: 'right',
    render: (row) => {
      const nodes = []
      // 导出完成且可下载：用浏览器原生下载，不 fetch blob（大文件会卡死标签页）
      if (row.kind === 'export' && row.download_ready) {
        nodes.push(
          h(
            'a',
            {
              class: 'task-download',
              href: imageDownloadUrl(row.id),
              // 由浏览器下载并落盘，不经 fetch
              download: ''
            },
            '下载'
          )
        )
      }
      // 只有未结束的任务才能取消
      if (isTaskActive(row)) {
        nodes.push(
          h(
            NButton,
            {
              size: 'tiny',
              quaternary: true,
              type: 'warning',
              loading: props.cancellingId === row.id,
              disabled: props.cancellingId !== '',
              onClick: () => emit('cancel', row)
            },
            { default: () => '取消' }
          )
        )
      }
      if (nodes.length === 0) {
        return h('span', { class: 'arkade-cell-muted' }, '—')
      }
      return h('div', { class: 'task-actions' }, nodes)
    }
  }
])
</script>

<template>
  <div class="image-tasks">
    <div class="image-tasks__head">
      <h2 class="image-tasks__title">
        镜像任务
        <span class="image-tasks__count" v-if="tasks.length > 0">{{ tasks.length }} 个</span>
      </h2>
      <n-button size="tiny" quaternary :loading="loading" @click="emit('refresh')">刷新</n-button>
    </div>

    <!--
      三条限制直接写在列表上方：这些都不是 bug，
      但用户不知道就会误判（以为任务丢了 / 链接坏了 / 取消能还原）。
    -->
    <p class="image-tasks__note">
      任务记录只保存在 Hub 内存中，Hub 重启后会清空（镜像本身已写入的内容不受影响）。
      导出的下载链接 <strong>30 分钟后过期</strong>，需登录访问并会重新校验权限。
      取消任务 <strong>不会回滚</strong>已完成的写入，导入/迁移可能已更新同名标签。
    </p>

    <n-data-table
      :columns="columns"
      :data="tasks"
      :loading="loading"
      :row-key="(row: ImageTask) => row.id"
      :scroll-x="1080"
      size="small"
      :max-height="320"
      virtual-scroll
    />

    <p v-if="tasks.length === 0 && !loading" class="image-tasks__empty">
      暂无镜像任务。拉取、导出、导入或迁移镜像后，进度会显示在这里。
    </p>
  </div>
</template>

<style scoped>
.image-tasks {
  border: 1px solid var(--arkade-border-color);
  border-radius: 8px;
  padding: 12px 14px;
  background: var(--arkade-page-bg);
}

.image-tasks__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 6px;
}

.image-tasks__title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--arkade-text-primary);
}

.image-tasks__count {
  margin-left: 6px;
  font-size: 12px;
  font-weight: 400;
  color: var(--arkade-text-tertiary);
}

.image-tasks__note {
  margin: 0 0 10px;
  font-size: 12px;
  line-height: 1.6;
  color: var(--arkade-text-tertiary);
}

.image-tasks__empty {
  margin: 8px 0 0;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.task-progress {
  display: flex;
  align-items: center;
  gap: 8px;
}

.task-progress__text {
  font-size: 12px;
  white-space: nowrap;
  color: var(--arkade-text-secondary);
}

.task-cell {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.task-phase {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-message {
  font-size: 12px;
  color: var(--arkade-text-secondary);
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.task-actions {
  display: flex;
  align-items: center;
  gap: 8px;
}

.task-download {
  font-size: 12px;
  color: var(--arkade-link-color, #2080f0);
  text-decoration: none;
}

.task-download:hover {
  text-decoration: underline;
}
</style>
