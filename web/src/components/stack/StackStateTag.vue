<script setup lang="ts">
/**
 * 编排状态标签
 *
 * 与容器状态标签（ContainerStateTag）分开的原因：
 * 编排是「一组服务」的整体状态，语义与单容器不同，
 * 尤其 `partial`（部分运行）在容器那里不存在。
 */
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import type { StackState } from '@/types/stack'

interface Props {
  state: StackState
}

const props = defineProps<Props>()

type TagType = 'success' | 'warning' | 'error' | 'default' | 'info'

interface StateMeta {
  label: string
  type: TagType
}

/**
 * 状态 → 文案 + 颜色。
 *
 * 用 Record + 兜底而不是 switch：后端将来加了状态值也不会渲染成空白，
 * 未知状态退化成原值，不会崩。
 *
 * offline 单独一类：Agent 离线时后端把每条编排都标成 offline，
 * 这不是「编排停止」，而是「查不到真实状态」，必须区分开，
 * 否则用户会以为自己的服务全被停了。
 */
const STATE_MAP: Record<string, StateMeta> = {
  running: { label: '运行中', type: 'success' },
  stopped: { label: '已停止', type: 'default' },
  // 部分运行是关键告警状态：说明只有部分服务起来了，需要用户介入
  partial: { label: '部分运行', type: 'warning' },
  offline: { label: '离线', type: 'info' },
  unknown: { label: '未知', type: 'info' }
}

const meta = computed<StateMeta>(() => STATE_MAP[props.state] ?? { label: props.state, type: 'info' })
</script>

<template>
  <n-tag size="small" :bordered="false" :type="meta.type">{{ meta.label }}</n-tag>
</template>
