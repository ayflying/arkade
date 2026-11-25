<script setup lang="ts">
/**
 * 存储卷列表页 /agents/:id/volumes
 *
 * 功能：列出卷（名称/驱动/挂载点/被哪些容器使用/标签），支持多选批量删除与一键清理。
 *
 * 删除卷是这个应用里**最危险**的操作之一：
 * 卷里装的是容器数据（数据库文件、上传文件等），删掉即永久丢失且无法恢复。
 * 因此确认文案必须把「不可恢复」说透，并且对「使用中」的卷直接排除出可删范围
 * （后端也会拒绝，但提前排除避免用户白点一次、也避免批量删除里混进必然失败的项）。
 *
 * ## 为什么行内删除按钮没了
 *
 * 一次只会删一个的单按钮，在「清出一批没人用的卷」这个真实用途上反而是最慢的。
 * 改成勾选 + 页头批量删除后，单个删除仍然可用（勾一个再删），
 * 而且危险操作永远要走同一个二次确认，不会两条路径文案不一致。
 */
import { computed, h, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { NButton, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import AgentLabelList from '@/components/agent/AgentLabelList.vue'
import ResourcePage from '@/components/resource/ResourcePage.vue'
import { deleteVolume, listVolumes } from '@/api/resources'
import { useBulkActions, describeTargets } from '@/composables/useBulkActions'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { clampPage, slicePage } from '@/composables/useLocalPager'
import { useResourceList } from '@/composables/useResourceList'
import type { VolumeInfo } from '@/types/resource'
import { confirmAction } from '@/utils/confirm'
import { toastError } from '@/utils/feedback'

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

const { items, loading, errorText, loaded, refresh } = useResourceList<VolumeInfo>({
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
 * 多选与批量删除。
 *
 * 行键用卷名：它就是删除接口的标识，也是界面上唯一的稳定身份。
 */
const bulk = useBulkActions<VolumeInfo>({
  items,
  keyOf: (item) => item.name,
  labelOf: (item) => item.name,
  removeOne: async (item) => {
    if (agentId.value === null) return
    await deleteVolume(agentId.value, item.name)
  },
  refresh,
  unit: '个卷'
})

/**
 * 分页：每页 20 个。只决定渲染哪一页，请求与统计仍基于全量。
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
  bulk.clear()
})

/** 使用中（被容器引用）的卷不能删 */
function isDeletable(item: VolumeInfo): boolean {
  return !item.in_use
}

/** 当前页里可被勾选的卷名：不可删的行直接不给勾选 */
const selectableOnPage = computed(() => pagedItems.value.filter(isDeletable).map((v) => v.name))

/** 已选中的卷对象（只含可删的） */
const selectedVolumes = computed(() =>
  items.value.filter((v) => bulk.checkedKeys.value.includes(v.name) && isDeletable(v))
)

/** 勾了却删不掉的数量（列表刚刷新、卷被别的容器挂上） */
const skippedCount = computed(() => bulk.checkedKeys.value.length - selectedVolumes.value.length)

/** 未被任何容器使用的卷：这就是「可清理」的全部候选 */
const unusedVolumes = computed(() => items.value.filter((v) => !v.in_use))

const subtitle = computed(() => {
  if (!loaded.value) return '正在加载存储卷…'
  const total = items.value.length
  const unused = unusedVolumes.value.length
  return `共 ${total} 个存储卷，未使用 ${unused} 个`
})

/**
 * 确认要删的卷。
 *
 * 二次确认文案刻意写重：
 * - 明确指出「卷内所有数据将被永久删除，无法恢复」；
 * - 逐个列出卷名，避免误删相邻的同类卷；
 * - 提示备份先行。
 */
async function confirmDelete(labels: string[]): Promise<boolean> {
  if (labels.length === 0) return false
  const single = labels.length === 1
  return confirmAction({
    title: single ? '确认删除存储卷？' : `确认删除 ${String(labels.length)} 个存储卷？`,
    content:
      (single ? `将删除存储卷「${labels[0]}」。` : `将删除以下存储卷：\n${describeTargets(labels)}`) +
      '\n\n卷内保存的所有数据（数据库文件、上传文件、日志等）会被永久删除，' +
      '此操作不可撤销、无法恢复。如果这些数据还有用，请先备份。',
    positiveText: single ? '永久删除' : `永久删除 ${String(labels.length)} 个`,
    negativeText: '取消',
    danger: true
  })
}

/** 批量删除已勾选的卷 */
async function handleBulkDelete(): Promise<void> {
  const targets = selectedVolumes.value
  if (targets.length === 0) return
  if (!(await confirmDelete(targets.map((v) => v.name)))) return
  await bulk.removeMany(
    targets.map((v) => ({ key: v.name, label: v.name })),
    skippedCount.value
  )
}

/**
 * 清理未使用的卷。
 *
 * 这是本页最省事的操作，也是最容易被误点的：一次可能删掉几十个卷。
 * 所以确认框里必须把「有多少个、叫什么」摆出来，而不是只说一句
 * 「确定清理吗」——用户对「一批」没有直觉，只有看到具体名字才知道怕。
 */
async function handleCleanup(): Promise<void> {
  const candidates = unusedVolumes.value
  if (candidates.length === 0) {
    toastError('没有可清理的存储卷')
    return
  }
  const labels = candidates.map((v) => v.name)
  const confirmed = await confirmAction({
    title: `清理未使用的存储卷（${String(labels.length)} 个）`,
    content:
      `以下存储卷没有被任何容器使用，将被永久删除：\n${describeTargets(labels)}\n\n` +
      '卷内数据会一并删除，不可撤销、无法恢复。若其中有暂时停用容器的数据卷，请先不要清理。',
    positiveText: `永久删除 ${String(labels.length)} 个`,
    negativeText: '取消',
    danger: true
  })
  if (!confirmed) return
  await bulk.cleanup(candidates.map((v) => ({ key: v.name, label: v.name })))
}

/** 表头全选：只作用于当前页（跨页累积选择容易误删） */
function togglePageSelection(): void {
  bulk.togglePage(selectableOnPage.value)
}

/**
 * 使用者的展示。
 *
 * 卷名旁边直接列出容器名，而不是只给一句「使用中」：
 * 用户要回答的问题永远是「到底谁在占着它」，判断能不能清掉。
 * 容器名多的折成「+N」，完整清单在 title 里。
 *
 * ## 为什么这里必须容忍 containers 缺失
 *
 * `containers` 是 0.1.40 才加的字段。滚动升级期间（Hub 已升、某些 Agent
 * 还没升）旧 Agent 不会返回它，Hub 会据此序列化成 `null`。
 * 若直接 `row.containers.length`，一次属性访问就把整个卷列表渲染打断——
 * 而这一页恰恰是升级后第一件要去确认的事。
 *
 * 更关键的是**不能把「使用中」显示成「未使用」**：`in_use` 是旧字段、
 * 一直可信。拿不到名字时如实说「使用中」，而不是兜底成空数组后
 * 谎称未使用——那会让人以为可以清理，而清理会真的删掉数据。
 */
function renderContainers(row: VolumeInfo): ReturnType<typeof h> {
  // null 与 undefined 都要挡：协议里没有 omitempty，跨版本时是 null
  const names = row.containers ?? []
  if (names.length === 0) {
    // in_use 为真但拿不到名字 = 旧版 Agent 未上报，不能据此说未使用
    if (row.in_use) {
      return h(
        NTag,
        {
          size: 'small',
          bordered: false,
          type: 'warning',
          title: '该服务器组件版本较旧，未上报具体容器名；升级组件后可看到容器列表'
        },
        { default: () => '使用中' }
      )
    }
    return h(
      NTag,
      { size: 'small', bordered: false, type: 'default' },
      { default: () => '未使用' }
    )
  }
  const shown = names.slice(0, 2)
  const hidden = names.length - shown.length
  const nodes = [
    h(
      NTag,
      { size: 'small', bordered: false, type: 'warning' },
      { default: () => shown[0] }
    )
  ]
  if (shown.length > 1) {
    nodes.push(
      h(
        NTag,
        { size: 'small', bordered: false, type: 'warning', style: 'margin-left:4px' },
        { default: () => shown[1] }
      )
    )
  }
  if (hidden > 0) {
    nodes.push(
      h(
        NTag,
        { size: 'small', bordered: false, style: 'margin-left:4px' },
        { default: () => `+${String(hidden)}` }
      )
    )
  }
  // 完整清单放 title：一行里塞不下五个长容器名，但排查时需要全部看到
  return h('span', { title: names.join('\n') }, nodes)
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
    type: 'selection',
    width: 40,
    // 使用中的卷删不掉，不给勾选：让「能勾的都能删」成为一条可依赖的直觉
    disabled: (row) => !isDeletable(row)
  },
  {
    title: '名称',
    key: 'name',
    minWidth: 300,
    /*
     * 卷名可点击进入文件管理器。
     *
     * 用真正的 <button> 承载而不是整行点击：整行点击会和勾选/展开类交互冲突，
     * 误点风险大。只让名称可点，目标明确，键盘也能用。
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
    title: '挂载到的容器',
    key: 'containers',
    minWidth: 220,
    render: renderContainers
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
    minWidth: 280,
    // 宿主机路径很长：等宽字体 + 省略号，完整路径放 title 提示
    render: (row) =>
      h('span', { class: 'arkade-cell-mono', title: row.mountpoint }, row.mountpoint)
  },
  {
    title: '标签',
    key: 'labels',
    minWidth: 200,
    /*
     * 只展示 2 个标签，其余折成「+N」。
     *
     * 之前用默认的 4 个：rustfs_data 这类编排卷带 4 个 compose 标签，
     * 在窄列里一个一行，直接把那一行撑成四行高，和相邻行完全不在一个节奏上。
     * 标签是次要信息，不该决定行高；完整内容在「+N」的悬停提示里。
     */
    render: (row) => h(AgentLabelList, { labels: row.labels, max: 2 })
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
    :scroll-x="1180"
    :row-key="(row: VolumeInfo) => row.name"
    :checked-keys="bulk.checkedKeys.value"
    @update:page="(value: number) => (page = value)"
    @update:checked-keys="(keys: string[]) => (bulk.checkedKeys.value = keys)"
    @refresh="refresh"
  >
    <template #actions>
      <!--
        清理：按规则扫全量（未使用即候选），不依赖当前选择与当前页。
        放在批量删除**左边**：批量删除作用于用户明确勾选的项，
        清理是范围更大的自动操作，让更精确的那个靠近手势起点。
      -->
      <n-button
        size="small"
        :disabled="!isOnline || unusedVolumes.length === 0 || bulk.busy.value"
        :loading="bulk.busy.value && bulk.checkedKeys.value.length === 0"
        title="删除所有没有被容器使用的存储卷"
        @click="void handleCleanup()"
      >
        清理未使用（{{ unusedVolumes.length }}）
      </n-button>
      <n-button
        size="small"
        type="error"
        secondary
        :disabled="selectedVolumes.length === 0 || bulk.busy.value"
        :loading="bulk.busy.value && bulk.checkedKeys.value.length > 0"
        @click="void handleBulkDelete()"
      >
        删除{{ selectedVolumes.length > 0 ? `（${selectedVolumes.length}）` : '' }}
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
    </template>
  </resource-page>
</template>

<!--
  这里刻意没有 <style scoped>。
  本页所有单元格都在传给 ResourcePage 的 render 函数里创建，
  scoped 样式匹配不到那些节点（详见 global.css 里 .arkade-cell-button 的说明），
  写了也只是看着像在工作、实际完全不生效。单元格样式一律走全局 arkade-* 类。
-->
