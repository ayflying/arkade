<script setup lang="ts">
import { onMounted, onUnmounted, ref, shallowRef } from 'vue';
import { NAlert, NButton, NSpace } from 'naive-ui';
import { getMonitorStatus, type MonitorStatus } from '@/api/monitor';
import MonitorMetrics from './MonitorMetrics.vue';
import MonitorHistory from './MonitorHistory.vue';
const status = shallowRef<MonitorStatus>({ metrics: [], incidents: [], deliveries: [] });
defineProps<{ intervalSeconds: number }>();
const now = ref(Date.now());
const busy = ref(false), error = ref(''), updatedAt = ref('');
let timer: ReturnType<typeof setTimeout> | undefined;
let mounted = false;
function stopTimer() { if (timer !== undefined) { clearTimeout(timer); timer = undefined; } }
function schedule() {
  stopTimer();
  if (mounted && !document.hidden) timer = setTimeout(refresh, 15000);
}
async function refresh() {
  if (!mounted || document.hidden || busy.value) return;
  stopTimer(); busy.value = true; now.value = Date.now();
  try {
    const value = await getMonitorStatus();
    if (!mounted) return;
    status.value = { metrics: value.metrics ?? [], incidents: value.incidents ?? [], deliveries: value.deliveries ?? [] };
    updatedAt.value = new Date().toLocaleTimeString('zh-CN');
    error.value = '';
  } catch (cause) {
    if (mounted) error.value = cause instanceof Error ? cause.message : '读取监控状态失败';
  } finally {
    now.value = Date.now();
    busy.value = false;
    schedule();
  }
}
function visibilityChanged() {
  stopTimer();
  if (!document.hidden) void refresh();
}
onMounted(() => {
  mounted = true;
  document.addEventListener('visibilitychange', visibilityChanged);
  void refresh();
});
onUnmounted(() => {
  mounted = false;
  stopTimer();
  document.removeEventListener('visibilitychange', visibilityChanged);
});
</script>
<template>
  <n-space vertical size="large">
    <n-space align="center">
      <h3>监控状态</h3>
      <n-button :loading="busy" :disabled="busy" @click="refresh">刷新状态</n-button>
      <span>{{ updatedAt ? `最近更新 ${updatedAt}` : '尚未获取状态' }}；页面可见时每 15 秒刷新，不影响上方输入。</span>
    </n-space>
    <n-alert v-if="error" type="error">{{ error }}。保留上次状态，数据可能已过期。</n-alert>
    <monitor-metrics :metrics="status.metrics" :interval-seconds="intervalSeconds" :now="now" />
    <monitor-history :incidents="status.incidents" :deliveries="status.deliveries" />
  </n-space>
</template>
