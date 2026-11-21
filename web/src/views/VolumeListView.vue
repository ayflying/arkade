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
import { computed, h, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import AgentLabelList from '@/components/agent/AgentLabelList.vue'
import ResourcePage from '@/components/resource/ResourcePage.vue'
import { deleteVolume, listVolumes } from '@/api/resources'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { clampPage, slicePage } from '@/composables/useLocalPager'
import { useResourceList } from '@/composables/useResourceList'
import type { VolumeInfo } from '@/types/resource'
import { confirmAction } from '@/utils/confirm'

const { agentId, isOnline } = useCurrentAgent()
const router = useRouter()

/**
 * 打开某个卷的文件管理器。
 *
 * 卷名要编码：卷名可能含空格、中文、`/`（虽然 docker 卷名不允许 `/`，
 * 但不该假设后端永远不会放宽）。`encodeURIComponent` 保证最坏情况
 * 也只是一个编码后的路径段，而不是被当成两级路径。
 */
async function openFiles(row: VolumeInfo): Promise<void> {
  if (agentId.value === null) return
  await router.push({
    name: 'agent-volume-files',
    params: { id: agentId.value, name: row.name }
  })
}

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

/**
 * 分页：每页 20 个。只决定渲染哪一页，请求与「使用中 N 个」统计仍基于全量。
 * 删到最后一页空掉时夹回合法页，切换服务器时回到第 1 页。
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

/**
 * 列定义。
 *
 * ⚠️ 这里的 render 里**只能用全局 arkade-* 类**，不能依赖本文件的 scoped 样式：
 * columns 传给 ResourcePage 后由它内部的 n-data-table 调用 render，
 * 执行上下文不是本组件，scoped 的 data-v 属性选择器匹配不到运行时节点，
 * 样式会静默失效。曾经卷名用的是 scoped 的 .volume-name-btn，结果
 * <button> 退回原生样式：灰底、边框、居中、自动换行，一排名字被折成三四行，
 * 整张表又高又乱。现在统一走 global.css 里的 .arkade-cell-button。
 *
 * 所有单元格都保持**单行**：这一页的卷名普遍很长（CI 留下的
 * GITEA-ACTIONS-TASK-… 动辄上百字符），一旦允许换行，行高就完全不可控，
 * 一屏看不掉几条。完整值一律放 title，悬停可见。
 */
const columns = computed<DataTableColumns<VolumeInfo>>(() => [
  {
    title: '名称',
    key: 'name',
    minWidth: 320,
    /*
     * 卷名可点击进入文件管理器。
     *
     * 用真正的 <button> 承载而不是整行点击：整行点击会和「选中/展开」类交互冲突，
     * 而且这一行还有删除按钮，误点风险大。只让名称可点，目标明确，键盘也能用。
     */
    render: (row) =>
      h(
        'button',
        {
          type: 'button',
          class: 'arkade-cell-button arkade-cell-name arkade-link',
          title: `${row.name}\n点击浏览卷内文件`,
          onClick: () => void openFiles(row)
        },
        row.name
      )
  },
  {
    title: '驱动',
    key: 'driver',
    // 主流就是 local，窄列即可；留宽了会挤掉真正需要空间的卷名
    width: 90
  },
  {
    title: '挂载点',
    key: 'mountpoint',
    minWidth: 320,
    // 宿主机路径很长：等宽字体 + 省略号，完整路径放 title 提示
    render: (row) =>
      h('span', { class: 'arkade-cell-mono', title: row.mountpoint }, row.mountpoint)
  },
  {
    title: '使用状态',
    key: 'in_use',
    width: 110,
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
    minWidth: 240,
    /*
     * 只展示 2 个标签，其余折成「+N」。
     *
     * 之前用默认的 4 个：rustfs_data 这类编排卷带 4 个 compose 标签，
     * 在窄列里一个一行，直接把那一行撑成四行高，和相邻行完全不在一个节奏上。
     * 标签是次要信息，不该决定行高；完整内容在「+N」的悬停提示里。
     */
    render: (row) => h(AgentLabelList, { labels: row.labels, max: 2 })
  },
  {
    title: '操作',
    key: 'actions',
    width: 90,
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
    :data="pagedItems"
    :loading="loading"
    :loaded="loaded"
    :error-text="errorText"
    :online="isOnline"
    :page="page"
    :item-count="items.length"
    unit="个"
    empty-description="这台服务器上还没有任何存储卷。容器启动后声明的数据卷会出现在这里。"
    :scroll-x="1220"
    :row-key="(row: VolumeInfo) => row.name"
    @update:page="(value: number) => (page = value)"
    @refresh="refresh"
  />
</template>

<!--
  这里刻意没有 <style scoped>。
  本页所有单元格都在传给 ResourcePage 的 render 函数里创建，
  scoped 样式匹配不到那些节点（详见 global.css 里 .arkade-cell-button 的说明），
  写了也只是看着像在工作、实际完全不生效。单元格样式一律走全局 arkade-* 类。
-->
