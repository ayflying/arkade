<script setup lang="ts" generic="T extends TableRowData">
/**
 * 服务器资源页通用外壳
 *
 * 镜像 / 网络 / 存储卷三个页面结构完全相同：
 * 标题 + 摘要 + 刷新 + 离线告警 + 错误提示 + 表格/空态。
 * 抽成外壳后各页面只需提供「列定义」和「空态文案」，
 * 不再重复三份几乎一样的模板与样式。
 *
 * 关于泛型约束 `T extends TableRowData`：
 * NDataTable 的 `data` 要求「带字符串索引签名的对象」数组，
 * 但 naive-ui 内部那个类型叫 RowData 且未对外导出，
 * 因此在 types/table.ts 里自行声明了等价的 TableRowData。
 * 必须从独立模块 import：泛型参数由编译器在模块层处理，
 * 看不到 `<script setup>` 内部声明的类型。
 *
 * 配套约定：types/resource.ts 里的 ImageInfo/NetworkInfo/VolumeInfo 用 `type`
 * 而非 `interface` 声明——interface 不产生隐式索引签名，会卡住这个约束；
 * type 别名则天然满足，页面里不必写 as 断言。
 *
 * 为什么离线告警放在外壳里：这三类资源都来自 Agent 实机，
 * 服务器离线时必然拉不到数据，提前说清原因比让用户看到一句
 * 笼统的「加载失败」友好得多。
 */
import { NAlert, NButton, NDataTable, NTag } from 'naive-ui'
import type { DataTableColumns } from 'naive-ui'
import EmptyState from '@/components/common/EmptyState.vue'
import PageHeader from '@/components/common/PageHeader.vue'
import type { TableRowData } from '@/types/table'

interface Props {
  /** 页面标题，如「本地镜像」 */
  title: string
  /** 副标题摘要，如「共 12 个镜像」 */
  subtitle: string
  /** 表格列定义 */
  columns: DataTableColumns<T>
  /** 表格数据 */
  data: T[]
  /** 加载中 */
  loading: boolean
  /** 是否已成功加载过（区分「确实为空」与「还没加载」） */
  loaded: boolean
  /** 加载错误文案 */
  errorText: string
  /** 服务器是否在线；false 时展示离线告警并禁用刷新 */
  online: boolean
  /** 没有任何数据时的说明文案 */
  emptyDescription: string
  /** 表格最小宽度（px）：窄屏靠它触发横向滚动 */
  scrollX: number
  /** 行唯一键 */
  rowKey: (row: T) => string
}

defineProps<Props>()

const emit = defineEmits<{
  (e: 'refresh'): void
}>()
</script>

<template>
  <div class="resource-page">
  <!--
    页头右侧：默认只有「刷新」。
    actions 插槽放在刷新**之前**，让操作按钮靠左、刷新靠最右，
    符合「主要操作 → 次要工具」的阅读顺序。
  -->
    <page-header :title="title" :subtitle="subtitle">
      <template #actions>
        <slot name="actions" />
        <n-button size="small" :disabled="!online" :loading="loading" @click="emit('refresh')">
          刷新
        </n-button>
      </template>
    </page-header>

    <!-- 服务器离线：这类资源都在实机上，离线必然拿不到 -->
    <div v-if="!online" class="resource-page__alert">
      <n-alert type="warning" :bordered="false">
        服务器当前离线，无法获取{{ title }}列表。请等待 Agent 恢复在线后刷新。
      </n-alert>
    </div>

    <!-- 加载错误（非离线原因） -->
    <div v-else-if="errorText !== ''" class="resource-page__alert resource-page__alert--row">
      <n-alert type="error" closable>{{ errorText }}</n-alert>
      <n-button size="small" quaternary @click="emit('refresh')">重试</n-button>
    </div>

    <!--
      表格容器：class 里的 arkade-table-scroll 提供 min-width: 0。
      缺了它，宽表格会把外层 flex 撑破，scroll-x 的横向滚动就失效了。
    -->
    <div class="table-card arkade-table-scroll">
      <n-data-table
        v-if="data.length > 0 || loading"
        :columns="columns"
        :data="data"
        :loading="loading"
        :row-key="rowKey"
        :bordered="false"
        :single-line="false"
        size="medium"
        :scroll-x="scrollX"
      />
      <empty-state v-else-if="loaded" :description="emptyDescription">
        <template #extra>
          <n-button size="small" @click="emit('refresh')">刷新</n-button>
        </template>
      </empty-state>
      <div v-else class="table-skeleton">
        <n-tag :bordered="false" size="small">正在加载…</n-tag>
      </div>
    </div>

    <!--
      表格下方的附加区域（目前只有镜像页用它放「镜像任务」列表）。
      放在表格之后而不是之前：主内容是资源列表，任务列表是进度信息，
      用户来这一页首先关心的是有哪些镜像。
    -->
    <div v-if="$slots.extra" class="resource-page__extra">
      <slot name="extra" />
    </div>
  </div>
</template>

<style scoped>
.resource-page__alert {
  margin-bottom: 16px;
}

/* 错误提示 + 重试按钮并排 */
.resource-page__alert--row {
  display: flex;
  align-items: center;
  gap: 8px;
}

.resource-page__alert--row :deep(.n-alert) {
  flex: 1;
}

.table-card {
  background: #fff;
  border-radius: 8px;
  padding: 4px;
}

.table-skeleton {
  padding: 32px;
  text-align: center;
}

/* 表格下方附加区：与表格拉开距离，避免看起来是表格的一部分 */
.resource-page__extra {
  margin-top: 16px;
}
</style>
