<script setup lang="ts">
/**
 * Agent 状态徽标
 *
 * 封装目的：让「在线/离线/升级中/已禁用」的文案与配色规则只在一处生效，
 * 列表页、概览页的列渲染都复用，避免每个表格各写一套。
 *
 * 为什么 `disabled` 用一个独立 prop 而不是塞进 status：
 * 禁用是管理员在 Hub 侧设置的开关，**与 Agent 的连接状态是两个正交维度**——
 * 一台服务器可以既离线又被禁用，也可以在线但被禁用。
 * 混进 status 会丢掉其中一个信息，所以两者分开传，由本组件决定优先展示谁。
 */
import { computed } from 'vue'
import StatusTag from '@/components/common/StatusTag.vue'
import type { Agent } from '@/types/agent'
import { mapAgentStatus } from '@/utils/statusMap'

interface Props {
  status: Agent['status']
  /** 是否已被管理员禁用；为 true 时优先展示「已禁用」 */
  disabled?: boolean
}

const props = withDefaults(defineProps<Props>(), { disabled: false })

const style = computed(() => mapAgentStatus(props.status))

/** 契约内只有三种状态；其余视为未知，给徽标加虚线提示 */
const isKnown = computed(() => ['online', 'offline', 'upgrading'].includes(props.status))

/**
 * 实际展示的文案类型。
 *
 * 禁用态用中性灰（default）而不是错误红：被禁用是管理员的主动决定，
 * 画成红色会让人以为服务器出故障了。
 */
const label = computed(() => (props.disabled ? '已禁用' : style.value.label))
const tagType = computed(() => (props.disabled ? 'default' : style.value.type))
</script>

<template>
  <status-tag
    :label="label"
    :type="tagType"
    :dot="true"
    :unknown="!disabled && !isKnown"
  />
</template>
