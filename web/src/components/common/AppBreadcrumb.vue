<script setup lang="ts">
/**
 * 顶栏面包屑
 *
 * 层级推导（改造后）：
 * - /agents                    → 服务器
 * - /agents/:id/containers     → 服务器 / <机器名> / 容器
 * - /agents/:id/stacks/:name   → 服务器 / <机器名> / 容器编排 / <编排名>
 *
 * 中间层「机器名 / 操作页」都可点击返回：
 * - 点机器名回到该服务器的容器页（它是默认操作页）；
 * - 点「容器编排」回到编排列表。
 *
 * 手机端：只显示最后一级，前面用省略号暗示还有上级，
 * 但**不丢失**层级信息——最后一级仍是全路径中最深的页面名。
 */
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import { NBreadcrumb, NBreadcrumbItem } from 'naive-ui'
import { useCurrentAgent } from '@/composables/useCurrentAgent'

interface Props {
  /** 手机档：只保留最后一级 */
  compact?: boolean
}

const props = withDefaults(defineProps<Props>(), { compact: false })

const route = useRoute()
const { agentId, displayName } = useCurrentAgent()

interface CrumbItem {
  label: string
  /** 可点击返回的目标；最后一级没有 */
  to?: string
}

/** 各操作页的中文名：与菜单文案保持一致，避免同一个东西两种叫法 */
const OPERATION_LABELS: Record<string, string> = {
  'agent-containers': '容器',
  'agent-stacks': '容器编排',
  'agent-stack-create': '新建编排',
  'agent-stack-detail': '编排详情',
  'agent-images': '本地镜像',
  'agent-networks': '网络',
  'agent-volumes': '存储卷'
}

/** 顶层页面（不依赖选中服务器）的路由名 → 面包屑文案 */
const TOP_LEVEL_LABELS: Record<string, string> = {
  overview: '概览',
  agents: '服务器',
  audit: '操作审计'
}

/** 完整层级：由路由 name 推导，与路由表保持同步 */
const allItems = computed<CrumbItem[]>(() => {
  const routeName = typeof route.name === 'string' ? route.name : ''

  if (agentId.value === null) {
    // 未选中服务器：只显示当前顶层页面的名字。
    // 不能一律写「服务器」——概览页和审计页会被误标成「服务器」，用户找不到自己在哪
    const topLabel = TOP_LEVEL_LABELS[routeName] ?? '概览'
    return [{ label: topLabel }]
  }

  const items: CrumbItem[] = [
    { label: '服务器', to: '/agents' },
    // 机器名可点回该服务器的默认操作页（容器）
    { label: displayName.value, to: `/agents/${encodeURIComponent(agentId.value)}/containers` }
  ]

  const operationLabel = OPERATION_LABELS[routeName]
  if (operationLabel !== undefined) {
    items.push({ label: operationLabel })
  }

  // 编排详情再补一级具体编排名（从 URL 取，避免详情页数据未加载时面包屑缺一级）
  if (routeName === 'agent-stack-detail') {
    const rawName = route.params.name
    const stackName = Array.isArray(rawName) ? rawName[0] : rawName
    if (typeof stackName === 'string' && stackName !== '') {
      items.push({ label: stackName })
    }
  }

  return items
})

/** 实际展示项：手机档只留最后一级 */
const visibleItems = computed<CrumbItem[]>(() =>
  props.compact ? allItems.value.slice(-1) : allItems.value
)

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
      <span v-else class="crumbs__current">{{ item.label }}</span>
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
  color: #2080f0;
  text-decoration: none;
}

.crums__link:hover {
  text-decoration: underline;
}

.crumbs__current {
  color: #4a4f56;
}

.crumbs__ellipsis {
  color: #b5b9c0;
}

/* 面包屑单级文字过长时省略，避免长机器名把顶栏撑破 */
.crumbs :deep(.n-breadcrumb-item) {
  white-space: nowrap;
}
</style>
