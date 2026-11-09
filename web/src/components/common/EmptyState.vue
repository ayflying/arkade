<script setup lang="ts">
/**
 * 空态提示（通用）
 *
 * 用于「没有服务器」「该服务器上没有容器」等场景。
 * 独立成组件是为了统一图标/间距，并且让列表页的模板只关心业务列。
 */
import { NEmpty } from 'naive-ui'

interface Props {
  description: string
  size?: 'small' | 'medium' | 'large'
}

withDefaults(defineProps<Props>(), {
  size: 'medium'
})
</script>

<template>
  <n-empty :description="description" :size="size" class="empty-block">
    <template v-if="$slots.extra" #extra>
      <slot name="extra" />
    </template>
  </n-empty>
</template>

<style scoped>
.empty-block {
  padding: 32px 0;
}
</style>
