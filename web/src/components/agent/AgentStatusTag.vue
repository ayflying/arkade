<script setup lang="ts">
/**
 * Agent 状态徽标
 *
 * 封装目的：让「在线/离线/升级中」的文案与配色规则只在一处生效，
 * 列表页的列渲染直接复用，避免每个表格各写一套。
 */
import { computed } from 'vue'
import StatusTag from '@/components/common/StatusTag.vue'
import type { Agent } from '@/types/agent'
import { mapAgentStatus } from '@/utils/statusMap'

interface Props {
  status: Agent['status']
}

const props = defineProps<Props>()

const style = computed(() => mapAgentStatus(props.status))

/** 契约内只有三种状态；其余视为未知，给徽标加虚线提示 */
const isKnown = computed(() => ['online', 'offline', 'upgrading'].includes(props.status))
</script>

<template>
  <status-tag
    :label="style.label"
    :type="style.type"
    :dot="true"
    :unknown="!isKnown"
  />
</template>
