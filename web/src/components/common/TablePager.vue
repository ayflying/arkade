<script setup lang="ts">
/**
 * 表格分页条
 *
 * 五张列表页（容器 / 编排 / 镜像 / 网络 / 存储卷）共用同一个分页控件，
 * 保证翻页样式、总数文案、是否隐藏的规则完全一致。
 *
 * ## 三条行为约定
 *
 * 1. **只有一页就不显示**：一页能放下还挂个「1/1」纯属噪音，
 *    与审计页的处理保持一致；
 * 2. **总数与区间由这里统一算**：调用方只给 `itemCount`，
 *    避免各页面各写一份 `共 N 个` 的文案（也就不会各写各的口径）；
 * 3. **页码受控**：`page` 由父组件的 `v-model:page` 持有，
 *    这样父组件能在数据变短时把页码夹回合法范围（见 useLocalPager）。
 *
 * 注意：本组件只负责「显示第几页」，不切数据。
 * 切片由父组件用 `slicePage()` 完成——分页条如果自己去切数组，
 * 父组件的统计（共几个、几个运行中）就会和表格内容对不上。
 */
import { computed, h } from 'vue'
import { NPagination } from 'naive-ui'
import { TABLE_PAGE_SIZE, totalPageCount } from '@/composables/useLocalPager'

interface Props {
  /** 总条数（全量，不是当前页） */
  itemCount: number
  /** 当前页，从 1 开始 */
  page: number
  /** 每页条数，默认 20；页面通常不用传 */
  pageSize?: number
  /** 资源量词，用于「共 52 个容器」这类文案 */
  unit?: string
}

const props = withDefaults(defineProps<Props>(), {
  pageSize: TABLE_PAGE_SIZE,
  unit: '条'
})

const emit = defineEmits<{
  (e: 'update:page', page: number): void
}>()

const totalPages = computed(() => totalPageCount(props.itemCount, props.pageSize))

/** 只有一页时整条隐藏：`<= 1` 覆盖空列表 */
const visible = computed(() => totalPages.value > 1)

/** 左侧文案：总数 + 当前显示区间，用户一眼知道「正在看第几到第几」 */
const rangeText = computed(() => {
  if (props.itemCount === 0) return `共 0 ${props.unit}`
  const start = (props.page - 1) * props.pageSize + 1
  const end = Math.min(props.page * props.pageSize, props.itemCount)
  return `共 ${props.itemCount} ${props.unit}，第 ${start}-${end} ${props.unit}`
})

function handleUpdatePage(next: number): void {
  emit('update:page', next)
}
</script>

<template>
  <div v-if="visible" class="table-pager">
    <!--
      用 NPagination 的 prefix 而不是把文案放在分页器外面：
      prefix 会跟着分页器一起下沉/居中对齐，窄屏换行时不会两边错位。
    -->
    <n-pagination
      :page="page"
      :page-size="pageSize"
      :item-count="itemCount"
      :page-slot="9"
      :prefix="() => h('span', { class: 'table-pager__range' }, rangeText)"
      size="small"
      @update:page="handleUpdatePage"
    />
  </div>
</template>

<style scoped>
.table-pager {
  display: flex;
  justify-content: flex-end;
  padding: 12px 8px 4px;
}

/* 窄屏：分页器与文案可能换行，靠 gap 留出呼吸空间 */
.table-pager :deep(.n-pagination) {
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.table-pager :deep(.table-pager__range) {
  font-size: 13px;
  color: var(--arkade-text-secondary);
}
</style>
