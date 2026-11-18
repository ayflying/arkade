<script setup lang="ts">
import { computed } from 'vue'
import type { ContainerMetricSample, ContainerMetricsStatus } from '@/types/containerMetrics'
import { containerMetricsStatusText, formatContainerMetrics } from '@/utils/containerMetrics'

const props = defineProps<{
  sample?: ContainerMetricSample
  group: 'resources' | 'network'
  stale: boolean
  emptyStatus: ContainerMetricsStatus
}>()

const values = computed(() => props.sample ? formatContainerMetrics(props.sample) : null)
const note = computed(() => props.group === 'resources' ? values.value?.resourceNote : values.value?.networkNote)
</script>

<template>
  <div v-if="values" class="metric-cell">
    <span v-if="stale" class="metric-cell__note metric-cell__stale">已过期</span>
    <span v-if="note" class="metric-cell__note">{{ note }}</span>
    <dl v-else class="metric-cell__values">
      <dt>{{ group === 'resources' ? 'CPU' : '接收' }}</dt>
      <dd>{{ group === 'resources' ? values.cpu : values.rxRate }}</dd>
      <dt>{{ group === 'resources' ? '内存' : '发送' }}</dt>
      <dd>{{ group === 'resources' ? values.memory : values.txRate }}</dd>
    </dl>
  </div>
  <span v-else class="metric-cell__note">{{ containerMetricsStatusText[emptyStatus] }}</span>
</template>

<style scoped>
.metric-cell__values {
  display: grid;
  grid-template-columns: max-content 1fr;
  gap: 2px 8px;
  align-items: baseline;
  margin: 0;
  white-space: nowrap;
}

.metric-cell__values dt,
.metric-cell__note {
  color: var(--arkade-text-secondary);
  font-size: 12px;
}

.metric-cell__values dd {
  margin: 0;
  font-variant-numeric: tabular-nums;
}

.metric-cell__stale {
  display: block;
  font-weight: 600;
}
</style>
