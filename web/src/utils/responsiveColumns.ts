import { computed, type Ref } from 'vue'
import type { DataTableColumns } from 'naive-ui'
import { useResponsive } from '@/composables/useResponsive'

/** 手机上让固定列随表滚动，避免宽操作列遮住信息；不删列、不改原数组。 */
export function useResponsiveColumns<T>(columns: Readonly<Ref<DataTableColumns<T>>>) {
  const { isMobile } = useResponsive()
  return computed<DataTableColumns<T>>(() => {
    if (!isMobile.value) return columns.value
    return columns.value.map((column) =>
      column.fixed ? { ...column, fixed: undefined } : column
    )
  })
}
