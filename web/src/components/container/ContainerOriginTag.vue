<script setup lang="ts">
/**
 * 容器来源徽标（arkade / adopted / discovered）
 *
 * 架构文档 4.2 强调来源标记是「接管」功能的基础，
 * 因此这里用颜色区分并给出中文解释：
 * - 方舟部署(info)：Arkade 创建，可自动重建
 * - 已接管(warning)：外部部署后被接管，编排只读
 * - 未接管(default)：仅被发现，仅展示与基础操作
 * 鼠标悬停展示含义说明，帮助用户理解为什么某些容器不可编排。
 */
import { computed } from 'vue'
import { NTooltip } from 'naive-ui'
import StatusTag from '@/components/common/StatusTag.vue'
import type { Container } from '@/types/container'
import { mapContainerOrigin } from '@/utils/statusMap'

interface Props {
  origin: Container['origin']
}

const props = defineProps<Props>()

const style = computed(() => mapContainerOrigin(props.origin))

/** 每种来源的一句话解释，与架构文档 4.2 表格保持一致 */
const ORIGIN_HINT: Record<string, string> = {
  arkade: '由 Arkade 下发编排创建，可自动重建、可改编排',
  adopted: '外部工具部署后被接管，编排为只读快照，不擅自重建',
  discovered: '仅被发现、尚未接管，只提供展示与基础操作'
}

const hint = computed(() => ORIGIN_HINT[props.origin] ?? '未知来源，请检查后端数据')
const isKnown = computed(() => props.origin in ORIGIN_HINT)
</script>

<template>
  <n-tooltip trigger="hover" placement="top">
    <template #trigger>
      <span class="origin-tag">
        <status-tag :label="style.label" :type="style.type" :unknown="!isKnown" />
      </span>
    </template>
    {{ hint }}
  </n-tooltip>
</template>

<style scoped>
/* tooltip 需要包一层 span，否则 tag 自身的 hover 事件会被 tooltip 吞掉一部分 */
.origin-tag {
  display: inline-flex;
  cursor: help;
}
</style>
