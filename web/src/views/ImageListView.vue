<script setup lang="ts">
/**
 * 本地镜像列表页 /agents/:id/images
 *
 * 功能：
 * - 列出镜像（标签/ID/大小/创建时间/引用容器数），支持删除；
 * - 顶部：拉取镜像、导入镜像；
 * - 行内：导出、迁移、删除；
 * - 页内镜像任务列表（进度轮询）。
 *
 * ## 删除的两个特殊点
 *
 * 1. **有容器引用**：默认会被后端拒绝。前端提供「强制删除」勾选，
 *    但必须让用户明确知道「强制删除会让引用它的容器无法再启动」；
 * 2. **无标签（悬空镜像）**：docker 里显示为 `<none>:<none>`，
 *    这类通常是构建残留，删除最安全，但要和正常镜像区分显示。
 *
 * ## 权限
 *
 * 拉取/导入/导出/迁移/删除都要求该服务器上的 manage 权限。
 * 前端**不按角色隐藏入口**——普通用户被授予 manage 后同样能用，
 * 而后端没有「查询我对某个服务器的权限档位」的接口，
 * 按 admin/user 角色猜会让有权限的用户永远看不到按钮。
 * 真正拦截在后端，403 由统一错误提示兜底。
 */
import { computed, h, onMounted, ref } from 'vue'
import { NButton, NTag, NTooltip } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import ResourcePage from '@/components/resource/ResourcePage.vue'
import ImageTaskList from '@/components/image/ImageTaskList.vue'
import ImagePullModal from '@/components/image/ImagePullModal.vue'
import ImageImportModal from '@/components/image/ImageImportModal.vue'
import ImageMigrateModal from '@/components/image/ImageMigrateModal.vue'
import { cancelImageTask, exportImage } from '@/api/imageTasks'
import { deleteImage, listImages } from '@/api/resources'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { useImageTasks } from '@/composables/useImageTasks'
import { useResourceList } from '@/composables/useResourceList'
import { useAgentsStore } from '@/stores/agents'
import type { ImageInfo } from '@/types/resource'
import type { ImageTask } from '@/types/imageTask'
import { confirmAction } from '@/utils/confirm'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'
import { formatBytes, shortImageId } from '@/utils/formatSize'
import { toastError, toastSuccess } from '@/utils/feedback'

const { agentId, isOnline } = useCurrentAgent()
const agentsStore = useAgentsStore()

const { items, loading, errorText, loaded, busyKey, refresh, runDelete } = useResourceList<ImageInfo>({
  load: async () => {
    if (agentId.value === null) return []
    const res = await listImages(agentId.value)
    return res.images
  },
  remove: async (item) => {
    if (agentId.value === null) return
    // 有容器引用的镜像必须走强制删除，否则后端会拒绝
    await deleteImage(agentId.value, item.id, item.containers > 0)
  },
  loadErrorMessage: '加载镜像列表失败',
  deleteSuccessMessage: '镜像已删除'
})

/* ───────────────────────── 任务列表 ───────────────────────── */

const imageTasks = useImageTasks(() => agentId.value)

/** 服务器 id → 名字，给任务列表显示目标服务器用 */
const agentNames = computed<Record<string, string>>(() => {
  const map: Record<string, string> = {}
  for (const agent of agentsStore.agents) {
    map[agent.id] = agent.name
  }
  return map
})

/** 正在取消的任务 id */
const cancellingId = ref('')

/** 取消任务：说清「不回滚」再执行 */
async function handleCancelTask(task: ImageTask): Promise<void> {
  const kindLabel = { pull: '拉取', export: '导出', import: '导入', migrate: '迁移' }[task.kind]
  const confirmed = await confirmAction({
    title: '取消这个任务？',
    content:
      '取消会停止继续执行，但**不会回滚**已经完成的写入：\n\n' +
      '· 已下载的镜像层会保留在磁盘上；\n' +
      '· 导入/迁移已写入的部分不会撤回，目标上可能已经更新了同名标签。\n\n' +
      '确定要取消吗？',
    positiveText: '取消任务',
    negativeText: '继续执行',
    danger: true
  })
  if (!confirmed) return

  cancellingId.value = task.id
  try {
    const updated = await cancelImageTask(task.id)
    imageTasks.upsert(updated)
    toastSuccess(`${kindLabel}任务已取消`)
  } catch (err) {
    toastError(err instanceof Error ? err.message : '取消任务失败')
  } finally {
    cancellingId.value = ''
  }
}

/* ───────────────────────── 弹窗状态 ───────────────────────── */

const pullShow = ref(false)
const importShow = ref(false)

/** 迁移弹窗：需要带上是哪个镜像 */
const migrateShow = ref(false)
const migrateReference = ref('')
const migrateSize = ref<number | null>(null)

/** 服务器选项（含离线状态），供迁移目标下拉使用 */
const agentOptions = computed(() =>
  agentsStore.agents.map((a) => ({
    id: a.id,
    name: a.name,
    online: a.status === 'online'
  }))
)

/**
 * 任务提交后：立刻插进列表顶部。
 *
 * 不在这里刷新镜像列表——任务刚提交，镜像还没写入完成。
 * 由 ImageTaskList 在任务结束时发 taskSettled 再刷新。
 */
function handleSubmitted(task: ImageTask): void {
  imageTasks.upsert(task)
}

/**
 * 任务结束时刷新镜像列表。
 *
 * 拉取/导入/迁移成功后镜像集合会变，但任务**进行中**刷新没有意义
 * （镜像还没写完，刷了也看不到变化，白白打后端）。
 * 所以只在任务从「未结束」变成「已结束」时刷一次。
 */
function onTaskSettled(task: ImageTask): void {
  if (task.status === 'succeeded' || task.status === 'canceled') {
    void refresh()
  }
}

/* ───────────────────────── 行内操作 ───────────────────────── */

/** 取镜像的展示名：优先第一个标签，无标签用短 ID */
function imageLabel(row: ImageInfo): string {
  return row.tags.length > 0 ? row.tags[0] : shortImageId(row.id)
}

/**
 * 导出镜像。
 *
 * 提交后由任务列表显示进度，完成后那一行出现「下载」链接。
 * 这里不直接下载——导出本身要跑一段时间，完成后才有文件。
 */
async function handleExport(row: ImageInfo): Promise<void> {
  if (agentId.value === null) return
  try {
    const res = await exportImage(agentId.value, { reference: imageLabel(row) })
    imageTasks.upsert(res.task)
    toastSuccess('已开始导出，完成后可在下方任务列表下载')
  } catch (err) {
    toastError(err instanceof Error ? err.message : '导出失败')
  }
}

/** 打开迁移弹窗 */
function handleMigrate(row: ImageInfo): void {
  migrateReference.value = imageLabel(row)
  migrateSize.value = row.size
  migrateShow.value = true
}

/**
 * 删除镜像。
 *
 * 有引用时的文案与实际后果必须一致：强制删除后那些容器会起不来，
 * 所以文案里直接写明，而不是笼统说一句「确定删除吗」。
 */
async function handleDelete(item: ImageInfo): Promise<void> {
  const label = imageLabel(item)
  const hasContainers = item.containers > 0

  const content = hasContainers
    ? `镜像「${label}」正被 ${item.containers} 个容器引用。\n\n` +
      '强制删除后，这些容器将无法再启动（需要重新拉取或重新构建镜像）。' +
      '确定要强制删除吗？'
    : `将删除镜像「${label}」。\n\n此操作不可撤销，但不会影响正在运行的容器。`

  const confirmed = await confirmAction({
    title: hasContainers ? '确认强制删除镜像？' : '确认删除镜像？',
    content,
    positiveText: hasContainers ? '强制删除' : '删除',
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return

  await runDelete(item, item.id)
}

/** 汇总：总数 + 悬空镜像数，让用户一眼看出构建残留有多少 */
const subtitle = computed(() => {
  if (!loaded.value) return '正在加载镜像…'
  const total = items.value.length
  const dangling = items.value.filter((i) => i.tags.length === 0).length
  const danglingText = dangling > 0 ? `，其中悬空镜像 ${dangling} 个` : ''
  return `共 ${total} 个镜像${danglingText}`
})

const columns = computed<DataTableColumns<ImageInfo>>(() => [
  {
    title: '标签',
    key: 'tags',
    minWidth: 220,
    render: (row) => {
      // 悬空镜像（无标签）：docker 里显示 <none>，这里明确标注，避免看起来像数据缺失
      if (row.tags.length === 0) {
        return h(
          NTag,
          { size: 'small', bordered: false, type: 'default' },
          { default: () => '悬空镜像' }
        )
      }
      // 一个镜像可能有多个标签：全部列出，最多显示 3 个，其余折叠
      const shown = row.tags.slice(0, 3)
      const hidden = row.tags.length - shown.length
      const tagNodes = shown.map((tag) =>
        h(
          NTag,
          { size: 'small', bordered: false, key: tag },
          { default: () => tag }
        )
      )
      if (hidden > 0) {
        tagNodes.push(
          h(
            NTooltip,
            { trigger: 'hover' },
            {
              trigger: () =>
                h(
                  NTag,
                  { size: 'small', bordered: false },
                  { default: () => `+${hidden}` }
                ),
              default: () => row.tags.slice(3).join('，')
            }
          )
        )
      }
      return h('div', { class: 'arkade-tag-group' }, tagNodes)
    }
  },
  {
    title: '镜像 ID',
    key: 'id',
    minWidth: 130,
    render: (row) =>
      h(
        'span',
        { class: 'arkade-cell-mono', title: row.id },
        shortImageId(row.id)
      )
  },
  {
    title: '大小',
    key: 'size',
    minWidth: 100,
    render: (row) => formatBytes(row.size)
  },
  {
    title: '创建时间',
    key: 'created',
    minWidth: 140,
    render: (row) =>
      // 相对时间好读，精确时间放 title
      h('span', { title: formatAbsoluteTime(row.created) }, formatRelativeTime(row.created))
  },
  {
    title: '引用容器',
    key: 'containers',
    minWidth: 100,
    render: (row) =>
      // 被引用时用警告色，提示用户删了会影响这些容器
      h(
        NTag,
        {
          size: 'small',
          bordered: false,
          type: row.containers > 0 ? 'warning' : 'default'
        },
        { default: () => `${row.containers} 个` }
      )
  },
  {
    title: '操作',
    key: 'actions',
    width: 200,
    fixed: 'right',
    render: (row) => {
      // 悬空镜像没有可引用的 tag，无法导出/迁移（后端按 reference 定位）
      const hasReference = row.tags.length > 0
      const disableReason = !isOnline.value
        ? '服务器离线，无法操作'
        : !hasReference
          ? '悬空镜像没有标签，无法按名称导出或迁移（可删除）'
          : ''

      const buttons = [
        h(
          NButton,
          {
            size: 'tiny',
            quaternary: true,
            disabled: disableReason !== '',
            onClick: () => void handleExport(row)
          },
          { default: () => '导出' }
        ),
        h(
          NButton,
          {
            size: 'tiny',
            quaternary: true,
            disabled: disableReason !== '',
            onClick: () => handleMigrate(row)
          },
          { default: () => '迁移' }
        ),
        h(
          NButton,
          {
            size: 'tiny',
            quaternary: true,
            type: 'error',
            disabled: busyKey.value !== '',
            loading: busyKey.value === row.id,
            onClick: () => void handleDelete(row)
          },
          { default: () => '删除' }
        )
      ]

      const group = h('div', { class: 'arkade-row-actions' }, buttons)
      // 禁用原因放 tooltip，避免用户不知道为什么点不了
      if (disableReason === '') return group
      return h(
        NTooltip,
        { trigger: 'hover' },
        {
          trigger: () => group,
          default: () => disableReason
        }
      )
    }
  }
])

onMounted(() => {
  void refresh()
})
</script>

<template>
  <resource-page
    title="本地镜像"
    :subtitle="subtitle"
    :columns="columns"
    :data="items"
    :loading="loading"
    :loaded="loaded"
    :error-text="errorText"
    :online="isOnline"
    empty-description="这台服务器上还没有本地镜像。可以拉取镜像，或导入 docker save 导出的 tar 文件。"
    :scroll-x="1150"
    :row-key="(row: ImageInfo) => row.id"
    @refresh="refresh"
  >
    <!-- 顶部操作：拉取 / 导入 -->
    <template #actions>
      <n-button size="small" :disabled="!isOnline" @click="importShow = true">导入镜像</n-button>
      <n-button size="small" type="primary" :disabled="!isOnline" @click="pullShow = true">
        拉取镜像
      </n-button>
    </template>

    <!-- 页内任务列表 -->
    <template #extra>
      <image-task-list
        :tasks="imageTasks.tasks.value"
        :loading="imageTasks.loading.value"
        :agent-id="agentId ?? ''"
        :agent-names="agentNames"
        :cancelling-id="cancellingId"
        @cancel="handleCancelTask"
        @refresh="imageTasks.refresh"
        @task-settled="onTaskSettled"
      />
    </template>
  </resource-page>

  <image-pull-modal
    v-model:show="pullShow"
    :agent-id="agentId ?? ''"
    :online="isOnline"
    @submitted="handleSubmitted"
  />
  <image-import-modal
    v-model:show="importShow"
    :agent-id="agentId ?? ''"
    :online="isOnline"
    @submitted="handleSubmitted"
  />
  <image-migrate-modal
    v-model:show="migrateShow"
    :agent-id="agentId ?? ''"
    :reference="migrateReference"
    :size-bytes="migrateSize"
    :agents="agentOptions"
    :online="isOnline"
    @submitted="handleSubmitted"
  />
</template>
