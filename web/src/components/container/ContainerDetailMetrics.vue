<script setup lang="ts">
import { computed } from 'vue'
import type { ContainerMetricSample, ContainerMetricsStatus } from '@/types/containerMetrics'
import { containerMetricsStatusText, formatContainerMetrics } from '@/utils/containerMetrics'
import { formatAbsoluteTime } from '@/utils/formatTime'

const props = defineProps<{
  sample?: ContainerMetricSample
  stale: boolean
  emptyStatus: ContainerMetricsStatus
}>()

const values = computed(() => props.sample ? formatContainerMetrics(props.sample) : null)
const sampleTime = computed(() => {
  const at = props.sample?.at ?? 0
  const date = new Date(at * 1000)
  return at > 0 && Number.isFinite(date.getTime())
    ? { label: formatAbsoluteTime(at), iso: date.toISOString() }
    : null
})
</script>

<template>
  <section class="detail-metrics">
    <h4>资源用量</h4>
    <div v-if="values" class="detail-metrics__groups">
      <dl>
        <dt>CPU</dt>
        <dd>{{ values.cpu }}</dd>
        <dt>内存</dt>
        <dd>{{ values.memory }}</dd>
      </dl>
      <dl>
        <dt>接收速率</dt>
        <dd>{{ values.rxRate }}</dd>
        <dt>发送速率</dt>
        <dd>{{ values.txRate }}</dd>
        <dt>累计接收</dt>
        <dd>{{ values.rxTotal }}</dd>
        <dt>累计发送</dt>
        <dd>{{ values.txTotal }}</dd>
      </dl>
      <p class="detail-metrics__note">
        采样时间：<time v-if="sampleTime" :datetime="sampleTime.iso">{{ sampleTime.label }}</time><span v-else>—</span>
        <strong v-if="stale">（已过期）</strong>
      </p>
      <p v-if="values.resourceNote || values.networkNote" class="detail-metrics__note">
        {{ [values.resourceNote, values.networkNote].filter(Boolean).join('；') }}
      </p>
    </div>
    <p v-else class="detail-metrics__note">{{ containerMetricsStatusText[emptyStatus] }}</p>
  </section>
</template>

<style scoped>
.detail-metrics h4 {
  margin: 0 0 8px;
  font-size: 13px;
  font-weight: 600;
}

.detail-metrics__groups {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.detail-metrics dl {
  margin: 0;
  display: grid;
  grid-template-columns: 88px minmax(0, 1fr);
  gap: 6px 12px;
  font-size: 13px;
}

.detail-metrics dd {
  margin: 0;
  font-variant-numeric: tabular-nums;
}

.detail-metrics dt,
.detail-metrics__note {
  color: var(--arkade-text-secondary);
}

.detail-metrics__note {
  margin: 0;
  font-size: 12px;
}
</style>
