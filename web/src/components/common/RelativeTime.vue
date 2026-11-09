<script setup lang="ts">
/**
 * 相对时间文本 + 精确时间 tooltip
 *
 * 需求：「最后心跳」要显示「3 分钟前」这类相对时间，但排查问题时又需要精确时刻。
 * 因此文本用相对时间，hover 显示绝对时间。
 *
 * 组件不做定时刷新：列表本身要手动刷新，相对时间不会长期挂着也不过期一整天，
 * 加个 30 秒的 setInterval 反而增加没人需要的复杂度（且大量行时有性能成本）。
 */
import { computed } from 'vue'
import { NTooltip } from 'naive-ui'
import { formatAbsoluteTime, formatRelativeTime, secondsToMs } from '@/utils/formatTime'

interface Props {
  /** Unix 秒级时间戳 */
  seconds: number
  /** tooltip 前缀文案，如「最后心跳」 */
  prefix?: string
}

const props = defineProps<Props>()

const valid = computed(() => secondsToMs(props.seconds) !== null)
const relative = computed(() => formatRelativeTime(props.seconds))
const absolute = computed(() => formatAbsoluteTime(props.seconds))
</script>

<template>
  <span class="relative-time">
    <n-tooltip v-if="valid" trigger="hover" placement="top">
      <template #trigger>
        <span class="relative-time__text">{{ relative }}</span>
      </template>
      {{ prefix ? `${prefix}：` : '' }}{{ absolute }}
    </n-tooltip>
    <!-- 没有心跳记录时显示占位符，不显示 1970 年这种误导性时间 -->
    <span v-else class="relative-time__empty">—</span>
  </span>
</template>

<style scoped>
.relative-time__text {
  cursor: default;
  border-bottom: 1px dashed #c9cdd4;
}

.relative-time__empty {
  color: #b5b9c0;
}
</style>
