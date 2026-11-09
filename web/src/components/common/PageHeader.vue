<script setup lang="ts">
/**
 * 页面标题栏（通用）
 *
 * 列表页统一结构：标题 + 副标题说明 + 右侧操作区（刷新等）。
 * 抽出来是为了两个页面视觉一致，避免重复写样式。
 */
interface Props {
  title: string
  /** 副标题：一句说明当前页在展示什么 */
  subtitle?: string
}

withDefaults(defineProps<Props>(), {
  subtitle: ''
})
</script>

<template>
  <div class="page-header">
    <div class="page-header__text">
      <h1 class="page-header__title">{{ title }}</h1>
      <p v-if="subtitle !== ''" class="page-header__subtitle">{{ subtitle }}</p>
    </div>
    <div class="page-header__actions">
      <slot name="actions" />
    </div>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.page-header__text {
  min-width: 0;
}

.page-header__title {
  margin: 0;
  font-size: 20px;
  font-weight: 600;
  line-height: 1.4;
  color: #1f2225;
}

.page-header__subtitle {
  margin: 4px 0 0;
  font-size: 13px;
  color: #8b9099;
}

.page-header__actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
</style>
