<script setup lang="ts">
import { computed } from 'vue'
import type { HostByteUsage } from '@/types/agent'
import { formatBytes } from '@/utils/formatSize'

const props = defineProps<{ label: string; cpuPercent?: number; usage?: HostByteUsage }>()
const percent = computed(() => props.usage ? props.usage.used_bytes / props.usage.total_bytes * 100 : props.cpuPercent)
const percentText = computed(() => percent.value === undefined ? '' : `${Number(percent.value.toFixed(1))}%`)
</script>

<template>
  <div class="host-usage">
    <div class="host-usage__reading">
      <span class="host-usage__label">{{ label }}</span>
      <strong v-if="percent !== undefined">{{ percentText }}</strong>
      <span v-else>无数据</span>
    </div>
    <template v-if="percent !== undefined">
      <div class="host-usage__track" aria-hidden="true">
        <div class="host-usage__fill" :style="{ width: `${percent}%` }" />
      </div>
      <span v-if="usage" class="host-usage__capacity">{{ formatBytes(usage.used_bytes) }} / {{ formatBytes(usage.total_bytes) }}</span>
    </template>
  </div>
</template>

<style scoped>
.host-usage { min-width: 0; color: var(--arkade-text-secondary); font-size: 12px; font-variant-numeric: tabular-nums; }
.host-usage__reading { display: flex; align-items: baseline; justify-content: space-between; gap: 4px 8px; flex-wrap: wrap; margin-bottom: 4px; }
.host-usage__reading strong { color: var(--arkade-text-primary); font-size: 14px; }
.host-usage__label { font-weight: 500; }
.host-usage__track { overflow: hidden; height: 4px; border-radius: 4px; background: var(--arkade-border-strong); }
.host-usage__fill { height: 100%; background: var(--arkade-primary); }
.host-usage__capacity { display: block; margin-top: 4px; }
</style>
