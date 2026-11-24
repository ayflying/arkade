<script setup lang="ts">
/**
 * 速率类指标读数（网络吞吐、磁盘读写）。
 *
 * 与 HostUsage 的分工：那边是「占总量多少」的容量指标，能画进度条；
 * 这里是没有上限的速率，画进度条会给出错误印象，所以只显示数值。
 * 缺失时显示「无数据」而不是 0 —— 0 B/s 是真实的「没有流量」，
 * 和「没采到」必须是两回事。
 */
import { formatBytes } from '@/utils/formatSize'

export type RateReading = { caption: string; bytesPerSec: number }

const props = defineProps<{ label: string; primary?: RateReading; secondary?: RateReading }>()

function formatRate(bytesPerSec: number): string {
  return `${formatBytes(bytesPerSec)}/s`
}
</script>

<template>
  <div class="host-rate">
    <div class="host-rate__reading">
      <span class="host-rate__label">{{ props.label }}</span>
      <strong v-if="props.primary"><span class="host-rate__caption">{{ props.primary.caption }}</span> {{ formatRate(props.primary.bytesPerSec) }}</strong>
      <span v-else>无数据</span>
    </div>
    <span v-if="props.secondary && props.primary" class="host-rate__secondary">
      <span class="host-rate__caption">{{ props.secondary.caption }}</span> {{ formatRate(props.secondary.bytesPerSec) }}
    </span>
  </div>
</template>

<style scoped>
.host-rate { min-width: 0; color: var(--arkade-text-secondary); font-size: 12px; font-variant-numeric: tabular-nums; }
.host-rate__reading { display: flex; align-items: baseline; justify-content: space-between; gap: 4px 8px; flex-wrap: wrap; margin-bottom: 4px; }
.host-rate__reading strong { color: var(--arkade-text-primary); font-size: 14px; }
.host-rate__label { font-weight: 500; }
.host-rate__caption { color: var(--arkade-text-secondary); font-weight: 400; }
.host-rate__secondary { display: block; }
</style>
