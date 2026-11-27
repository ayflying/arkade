<script setup lang="ts">
/**
 * 本地镜像列表页 /agents/:id/images
 *
 * 功能：
 * - 列出镜像（标签/ID/大小/创建时间/引用容器数），支持多选批量删除与一键清理；
 * - 顶部：拉取镜像、导入镜像、清理悬空镜像；
 * - 行内：导出、迁移；
 * - 页内镜像任务列表（进度轮询）。
 *
 * ## 删除的两个特殊点
 *
 * 1. **有容器引用**：默认会被后端拒绝。前端提供「强制删除」勾选，
 *    但必须让用户明确知道「强制删除会让引用它的容器无法再启动」；
 * 2. **无标签（悬空镜像）**：docker 里显示为 `<none>:<none>`，
 *    这类通常是构建残留，删除最安全，但要和正常镜像区分显示。
 *
 * ## 批量删除为什么单独设一个勾选项
 *
 * 强制删除在单个删除里是「用户明确勾了才做」，批量时如果自动降级成
 * 强制删除，就等于把一批有引用的镜像静默删掉——那些容器会集体起不来，
 * 而用户只勾了几行。因此批量删除默认**跳过**有引用的镜像并如实报告，
 * 只有用户主动勾了「同时强制删除」才动它们。
 *
 * ## 权限
 *
 * 拉取/导入/导出/迁移/删除都要求该服务器上的 manage 权限。
 * 前端**不按角色隐藏入口**——普通用户被授予 manage 后同样能用，
 * 而后端没有「查询我对某个服务器的权限档位」的接口，
 * 按 admin/user 角色猜会让有权限的用户永远看不到按钮。
 * 真正拦截在后端，403 由统一错误提示兜底。
 */
import { computed, h, onMounted, ref, watch } from 'vue'
import { NButton, NCheckbox, NTag, NTooltip } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import ResourcePage from '@/components/resource/ResourcePage.vue'
import ImageTaskList from '@/components/image/ImageTaskList.vue'
import ImagePullModal from '@/components/image/ImagePullModal.vue'
import ImageImportModal from '@/components/image/ImageImportModal.vue'
import ImageMigrateModal from '@/components/image/ImageMigrateModal.vue'
import { cancelImageTask, exportImage } from '@/api/imageTasks'
import { deleteImage, listImages } from '@/api/resources'
import { describeTargets, useBulkActions } from '@/composables/useBulkActions'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { useImageTasks } from '@/composables/useImageTasks'
import { clampPage, slicePage } from '@/composables/useLocalPager'
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

const { items, loading, errorText, loaded, refresh } = useResourceList<ImageInfo>({
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

/**
 * 批量删除。
 *
 * 行键用完整镜像 ID（与表格 row-key 一致）：短 ID 可能撞车，
 * 而删除接口收的是完整 ID。
 *
 * `removeOne` 里 force 固定为 false：批量路径绝不静默强制删除。
 * 有引用的镜像会被后端拒绝并计入失败清单，用户能看到是哪一个。
 * 这条约束由 handleBulkDelete 的勾选项控制，见下方说明。
 */
const bulkForce = ref(false)
const bulk = useBulkActions<ImageInfo>({
  items,
  keyOf: (item) => item.id,
  labelOf: (item) => imageLabel(item),
  removeOne: async (item) => {
    if (agentId.value === null) return
    await deleteImage(agentId.value, item.id, bulkForce.value && item.containers > 0)
  },
  refresh,
  unit: '个镜像'
})

/**
 * 分页：每页 20 个（TABLE_PAGE_SIZE）。
 *
 * 镜像页还有「镜像任务」列表（不分页，属于进度信息），
 * 这里的分页只作用于镜像表格本身。
 * 悬空镜像统计用全量 items，不受当前页影响。
 */
const page = ref(1)
const pagedItems = computed(() => slicePage(items.value, page.value))

watch(
  () => items.value.length,
  (count) => {
    page.value = clampPage(page.value, count)
  }
)

watch(agentId, () => {
  page.value = 1
  bulk.clear()
  bulkForce.value = false
})

/**
 * 可清理的镜像：悬空（无标签）且无容器引用。
 *
 * 为什么只清悬空镜像：已打标签但没有容器引用的镜像**不是垃圾**——
 * 它们是你准备重新部署时要用的。清理它们等于让「下次起容器」变成
 * 一次必然的重新拉取。悬空镜像则相反：没有任何标签能再引用到它，
 * 除了占磁盘没有别的用途。
 */
const cleanableImages = computed(() =>
  items.value.filter((i) => i.tags.length === 0 && i.container_names?.length === 0)
)

/** 当前页所有镜像都可勾选（镜像删除对单个而言总能做，是否强制由勾选项决定） */
const selectableOnPage = computed(() => pagedItems.value.map((i) => i.id))

/** 已选中的镜像 */
const selectedImages = computed(() =>
  items.value.filter((i) => bulk.checkedKeys.value.includes(i.id))
)

/** 已选中里带容器引用的数量：批量删除时会被跳过（除非勾了强制） */
const referencedSelected = computed(() => selectedImages.value.filter((i) => i.containers > 0))

/** 表头全选：只作用于当前页 */
function togglePageSelection(): void {
  bulk.togglePage(selectableOnPage.value)
}

/**
 * 批量删除已勾选的镜像。
 *
 * 有引用的镜像默认**跳过**：批量操作里静默强制删除会让一批容器集体起不来，
 * 而用户只勾了几行。要动它们必须显式勾选「同时强制删除」。
 */
async function handleBulkDelete(): Promise<void> {
  const all = selectedImages.value
  if (all.length === 0) return

  const skipped = bulkForce.value ? [] : referencedSelected.value
  const targets = all.filter((i) => !skipped.includes(i))
  if (targets.length === 0) {
    toastError('选中的镜像都被容器引用，请勾选「同时强制删除」或先删除相关容器')
    return
  }

  const forced = targets.filter((i) => i.containers > 0)
  const notes: string[] = []
  if (skipped.length > 0) {
    notes.push(`已跳过 ${String(skipped.length)} 个被容器引用的镜像（未勾选强制删除）：${skipped.map((i) => imageLabel(i)).join('、')}`)
  }
  if (forced.length > 0) {
    notes.push(`其中 ${String(forced.length)} 个镜像正被容器引用，强制删除后那些容器将无法再启动。`)
  }

  const confirmed = await confirmAction({
    title: `确认删除 ${String(targets.length)} 个镜像？`,
    content:
      `将删除以下镜像：\n${describeTargets(targets.map((i) => imageLabel(i)))}\n\n` +
      (notes.length > 0 ? `${notes.join('\n')}\n\n` : '') +
      '此操作不可撤销。已删除的镜像需要重新拉取或重新构建才能使用。',
    positiveText: `删除 ${String(targets.length)} 个`,
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return

  await bulk.removeMany(
    targets.map((i) => ({ key: i.id, label: imageLabel(i) })),
    skipped.length
  )
}

/**
 * 清理悬空镜像。
 *
 * 只清「无标签且无容器引用」的镜像，也就是 docker 里那些 <none> 构建残留。
 * 已打标签的镜像一律不动——那是用户还要用的东西。
 */
async function handleCleanup(): Promise<void> {
  const candidates = cleanableImages.value
  if (candidates.length === 0) {
    toastError('没有可清理的悬空镜像')
    return
  }
  const total = candidates.reduce((sum, i) => sum + i.size, 0)
  const labels = candidates.map((i) => shortImageId(i.id))
  const confirmed = await confirmAction({
    title: `清理悬空镜像（${String(candidates.length)} 个）`,
    content:
      `以下悬空镜像没有任何标签、也没有容器引用，共占用约 ${formatBytes(total)}：\n` +
      `${describeTargets(labels)}\n\n` +
      '它们通常是构建或拉取留下的中间层。删除后不可恢复，但不会影响任何带标签的镜像与容器。',
    positiveText: `删除 ${String(candidates.length)} 个`,
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return
  await bulk.cleanup(candidates.map((i) => ({ key: i.id, label: shortImageId(i.id) })))
}

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

/** 汇总：总数 + 悬空镜像数，让用户一眼看出构建残留有多少 */
const subtitle = computed(() => {
  if (!loaded.value) return '正在加载镜像…'
  const total = items.value.length
  const dangling = cleanableImages.value.length
  const danglingText = dangling > 0 ? `，其中悬空镜像 ${dangling} 个` : ''
  return `共 ${total} 个镜像${danglingText}`
})

const columns = computed<DataTableColumns<ImageInfo>>(() => [
  {
    type: 'selection',
    width: 40
  },
  {
    title: '镜像名称 / 标签',
    key: 'tags',
    minWidth: 220,
    render: (row) => {
      const labels = row.tags.length > 0 ? row.tags : (row.repositories ?? [])
      const shown = labels.slice(0, 3)
      const hidden = labels.length - shown.length
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
              default: () => labels.slice(3).join('，')
            }
          )
        )
      }
      if (row.tags.length === 0) {
        if (labels.length === 0) tagNodes.push(h(NTag, { size: 'small' }, { default: () => shortImageId(row.id) }))
        tagNodes.push(h(NTag, { size: 'small', bordered: false }, { default: () => '无标签' }))
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
    minWidth: 180,
    render: (row) => {
      const names = row.container_names
      if (names == null) return '引用状态未知'
      if (names.length === 0) return '未被引用'
      const nodes = names.slice(0, 2).map((name) =>
        h(NTag, { key: name, size: 'small', bordered: false, type: 'warning' }, { default: () => name })
      )
      if (names.length > 2) {
        nodes.push(h(NTag, { size: 'small', bordered: false }, { default: () => `+${names.length - 2}` }))
      }
      return h('div', { class: 'arkade-tag-group', title: names.join('、') }, nodes)
    }
  },
  {
    title: '操作',
    key: 'actions',
    width: 140,
    fixed: 'right',
    render: (row) => {
      // 悬空镜像没有可引用的 tag，无法导出/迁移（后端按 reference 定位）
      const hasReference = row.tags.length > 0
      const disableReason = !isOnline.value
        ? '服务器离线，无法操作'
        : !hasReference
          ? '悬空镜像没有标签，无法按名称导出或迁移（可在清理里删除）'
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
    :data="pagedItems"
    :loading="loading"
    :loaded="loaded"
    :error-text="errorText"
    :online="isOnline"
    :page="page"
    :item-count="items.length"
    unit="个"
    empty-description="这台服务器上还没有本地镜像。可以拉取镜像，或导入 docker save 导出的 tar 文件。"
    :scroll-x="1150"
    :row-key="(row: ImageInfo) => row.id"
    :checked-keys="bulk.checkedKeys.value"
    @update:page="(value: number) => (page = value)"
    @update:checked-keys="(keys: string[]) => (bulk.checkedKeys.value = keys)"
    @refresh="refresh"
  >
    <!-- 顶部操作：拉取 / 导入 / 清理悬空镜像 / 批量删除 -->
    <template #actions>
      <n-button
        size="small"
        :disabled="!isOnline || cleanableImages.length === 0 || bulk.busy.value"
        :loading="bulk.busy.value && bulk.checkedKeys.value.length === 0"
        title="删除所有没有标签、也没有容器引用的悬空镜像"
        @click="void handleCleanup()"
      >
        清理悬空（{{ cleanableImages.length }}）
      </n-button>
      <n-button
        size="small"
        type="error"
        secondary
        :disabled="selectedImages.length === 0 || bulk.busy.value"
        :loading="bulk.busy.value && bulk.checkedKeys.value.length > 0"
        @click="void handleBulkDelete()"
      >
        删除{{ selectedImages.length > 0 ? `（${selectedImages.length}）` : '' }}
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
      <!--
        强制删除开关：只在勾了「被容器引用」的镜像时才需要。
        默认关闭，并且在页头明写它的后果——这是批量操作里唯一能造成
        「一批容器起不来」的开关，不能靠 tooltip 藏着。
      -->
      <n-checkbox
        v-if="referencedSelected.length > 0"
        :checked="bulkForce"
        :disabled="bulk.busy.value"
        @update:checked="(value: boolean) => (bulkForce = value)"
      >
        同时强制删除（{{ referencedSelected.length }} 个被引用）
      </n-checkbox>
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
