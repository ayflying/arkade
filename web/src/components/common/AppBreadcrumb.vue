<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { NBreadcrumb, NBreadcrumbItem } from 'naive-ui'
import { useCurrentAgent } from '@/composables/useCurrentAgent'
import { buildBreadcrumbs } from '@/router/breadcrumbs'

interface Props {
  /** 手机档：只保留最后一级 */
  compact?: boolean
}

const props = withDefaults(defineProps<Props>(), { compact: false })

const route = useRoute()
const router = useRouter()
const { agentId, displayName } = useCurrentAgent()

const allItems = computed(() => buildBreadcrumbs(
  route,
  agentId.value === null ? null : { id: agentId.value, label: displayName.value },
  (to) => router.resolve(to).meta
))

// 手机端保持现有紧凑布局，只展示当前页。
const visibleItems = computed(() => props.compact ? allItems.value.slice(-1) : allItems.value)

/** 手机档且确实被裁剪时，在最后一级前加点号提示 */
const hasHiddenPrefix = computed<boolean>(() => props.compact && allItems.value.length > 1)
</script>

<template>
  <n-breadcrumb class="crumbs">
    <n-breadcrumb-item v-if="hasHiddenPrefix">
      <span class="crumbs__ellipsis">…</span>
    </n-breadcrumb-item>
    <n-breadcrumb-item v-for="(item, index) in visibleItems" :key="index">
      <router-link v-if="item.to" :to="item.to" class="crumbs__link">{{ item.label }}</router-link>
      <span v-else class="crumbs__current" :aria-current="index === visibleItems.length - 1 ? 'page' : undefined">{{ item.label }}</span>
    </n-breadcrumb-item>
  </n-breadcrumb>
</template>

<style scoped>
.crumbs {
  font-size: 13px;
  /* 面包屑可能在窄屏被挤压：允许收缩并隐藏溢出，不撑破顶栏 */
  min-width: 0;
  overflow: hidden;
}

.crumbs__link {
  color: var(--arkade-primary);
  text-decoration: none;
}

.crumbs__link:hover {
  text-decoration: underline;
}

.crumbs__current {
  color: var(--arkade-text-secondary);
}

.crumbs__ellipsis {
  color: var(--arkade-text-disabled);
}

/* 面包屑单级文字过长时省略，避免长机器名把顶栏撑破 */
.crumbs :deep(.n-breadcrumb-item) {
  white-space: nowrap;
}
</style>
