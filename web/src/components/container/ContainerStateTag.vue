<script setup lang="ts">
/**
 * 容器状态徽标
 *
 * 与 AgentStatusTag 同样只做「映射 + 渲染」，映射规则在 utils/statusMap。
 * 未知 state 直接显示后端原值：运维同学看到 paused 之类的原词，比一个
 * 被臆测翻译过的中文更有助于排查。
 */
import { computed } from 'vue'
import StatusTag from '@/components/common/StatusTag.vue'
import type { Container } from '@/types/container'
import { mapContainerState } from '@/utils/statusMap'

interface Props {
  state: Container['state']
}

const props = defineProps<Props>()

const style = computed(() => mapContainerState(props.state))
</script>

<template>
  <status-tag :label="style.label" :type="style.type" :dot="style.type === 'success'" />
</template>
