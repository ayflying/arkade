<script setup lang="ts">
/**
 * 状态徽标（通用）
 *
 * 为什么做成通用组件：Agent 状态、容器状态、容器来源都需要「彩色小标签」，
 * 各页面各写一遍 n-tag + 映射逻辑必然重复。这里只接收已算好的 label/type，
 * 映射规则统一放在 utils/statusMap.ts，保证口径一致。
 */
import { NTag } from 'naive-ui'
import type { TagType } from '@/utils/statusMap'

interface Props {
  /** 中文文案 */
  label: string
  /** Naive UI 颜色类型 */
  type?: TagType
  /** 是否在文字前加状态圆点（在线/离线类更直观） */
  dot?: boolean
  /** 徽标尺寸 */
  size?: 'small' | 'medium'
  /** 未知值的视觉标记：用虚线底色提示「这是没识别的状态」 */
  unknown?: boolean
}

withDefaults(defineProps<Props>(), {
  type: 'default',
  dot: false,
  size: 'small',
  unknown: false
})
</script>

<template>
  <n-tag :type="type" :size="size" round :class="{ 'status-tag--unknown': unknown }">
    <span class="status-tag__inner">
      <span v-if="dot" class="status-tag__dot" />
      <span>{{ label }}</span>
    </span>
  </n-tag>
</template>

<style scoped>
.status-tag__inner {
  display: inline-flex;
  align-items: center;
  gap: 4px;
}

.status-tag__dot {
  display: inline-block;
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: currentColor;
}

/* 未知值：虚线描边，避免用户把灰色徽标误读为「已停用」 */
.status-tag--unknown {
  border-style: dashed;
  border-width: 1px;
  border-color: #c9cdd4;
  opacity: 0.85;
}
</style>
