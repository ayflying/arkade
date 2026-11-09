<script setup lang="ts">
/**
 * 本地镜像列表页 /agents/:id/images
 *
 * 功能：列出镜像（ID/标签/大小/创建时间/引用容器数），支持删除。
 *
 * 删除的两个特殊点：
 * 1. **有容器引用**：默认会被后端拒绝。前端提供「强制删除」勾选，
 *    但必须让用户明确知道「强制删除会让引用它的容器无法再启动」；
 * 2. **无标签（悬空镜像）**：docker 里显示为 `<none>:<none>`，
 *    这类通常是构建残留，删除最安全，但要和正常镜像区分显示。
 */
import { computed, h, onMounted } from 'vue'
import { NButton, NTag, NTooltip } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import ResourcePage from '@/components/resource/ResourcePage.vue'
import { deleteImage, listImages } from '@/api/resources'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { useResourceList } from '@/composables/useResourceList'
import type { ImageInfo } from '@/types/resource'
import { confirmAction } from '@/utils/confirm'
import { formatAbsoluteTime, formatRelativeTime } from '@/utils/formatTime'
import { formatBytes, shortImageId } from '@/utils/formatSize'

const { agentId, isOnline } = useCurrentAgent()

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

/** 汇总：总数 + 悬空镜像数，让用户一眼看出构建残留有多少 */
const subtitle = computed(() => {
  if (!loaded.value) return '正在加载镜像…'
  const total = items.value.length
  const dangling = items.value.filter((i) => i.tags.length === 0).length
  const danglingText = dangling > 0 ? `，其中悬空镜像 ${dangling} 个` : ''
  return `共 ${total} 个镜像${danglingText}`
})

/**
 * 删除镜像。
 *
 * 有引用时的文案与实际后果必须一致：强制删除后那些容器会起不来，
 * 所以文案里直接写明，而不是笼统说一句「确定删除吗」。
 */
async function handleDelete(item: ImageInfo): Promise<void> {
  const label = item.tags.length > 0 ? item.tags[0] : shortImageId(item.id)
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
    width: 100,
    fixed: 'right',
    render: (row) =>
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
    empty-description="这台服务器上还没有本地镜像。编排启动时会自动拉取所需镜像。"
    :scroll-x="1050"
    :row-key="(row: ImageInfo) => row.id"
    @refresh="refresh"
  />
</template>
