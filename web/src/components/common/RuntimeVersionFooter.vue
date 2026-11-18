<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { fetchRuntimeVersion } from '@/api/runtimeVersion'

const label = ref('正在读取版本…')
const controller = new AbortController()

onMounted(async () => {
  const timeout = window.setTimeout(() => controller.abort(), 5000)
  try {
    const version = await fetchRuntimeVersion(controller.signal)
    label.value = version === null ? '版本暂不可用' : `当前运行版本：${version}`
  } finally {
    window.clearTimeout(timeout)
  }
})
onUnmounted(() => controller.abort())
</script>

<template>
  <footer class="runtime-version" aria-live="polite">{{ label }}</footer>
</template>

<style scoped>
.runtime-version {
  flex-shrink: 0;
  padding: 6px 12px calc(6px + env(safe-area-inset-bottom, 0px));
  border-top: 1px solid var(--arkade-border-color);
  background: var(--arkade-surface-bg);
  color: var(--arkade-text-secondary);
  font-size: 12px;
  text-align: center;
  overflow-wrap: anywhere;
}
</style>
