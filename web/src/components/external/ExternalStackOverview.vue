<script setup lang="ts">
/**
 * 外部项目概览
 *
 * 只展示状态、自动接管情况、服务/容器数。
 * 单独成组件是为了让详情页保持短文件——页面本身只负责组织 tab 与动作。
 */
import { computed } from 'vue'
import StackStateTag from '@/components/stack/StackStateTag.vue'
import AutoAdoptStatus from '@/components/stack/AutoAdoptStatus.vue'
import type { ExternalStack } from '@/types/stack'

interface Props {
  project: ExternalStack
  containerCount: number
}

const props = defineProps<Props>()

/** 服务数：null 表示未知，不能显示成 0 个 */
const serviceText = computed(() => {
  const list = props.project.services
  if (!Array.isArray(list)) return '未知'
  return `${String(list.length)} 个`
})
</script>

<template>
  <div class="ext-overview">
    <div class="ext-overview__item">
      <span class="ext-overview__label">状态</span>
      <stack-state-tag :state="project.status" />
    </div>
    <div class="ext-overview__item">
      <span class="ext-overview__label">自动接管</span>
      <auto-adopt-status
        :status="project.auto_adopt_status"
        :reason="project.auto_adopt_reason ?? ''"
      />
    </div>
    <div class="ext-overview__item">
      <span class="ext-overview__label">服务</span>
      <span class="ext-overview__value">{{ serviceText }}</span>
    </div>
    <div class="ext-overview__item">
      <span class="ext-overview__label">容器</span>
      <span class="ext-overview__value">{{ containerCount }} 个</span>
    </div>
  </div>
</template>

<style scoped>
.ext-overview {
  display: flex;
  flex-wrap: wrap;
  gap: 20px;
  padding: 4px 0;
}

.ext-overview__item {
  display: flex;
  align-items: center;
  gap: 8px;
}

.ext-overview__label {
  font-size: 13px;
  color: var(--arkade-text-secondary);
}

.ext-overview__value {
  font-size: 13px;
  color: var(--arkade-text-primary);
}
</style>
