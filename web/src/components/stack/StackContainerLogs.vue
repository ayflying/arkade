<script setup lang="ts">
/**
 * 外部项目的容器日志面板
 *
 * 复用现有容器日志接口（`fetchContainerLogs`），不新增 external/logs 端点：
 * 日志本来就是容器维度的事，外部项目只是提供「从哪个容器看」的入口。
 *
 * 独立成组件而不是写在页面里：日志有自己的容器选择、加载态、竞态清理，
 * 混进页面会让那个文件同时管三件事。
 */
import { onUnmounted, ref, watch } from 'vue'
import { NButton } from 'naive-ui'
import { fetchContainerLogs } from '@/api/containers'
import type { Container } from '@/types/container'

interface Props {
  agentId: string
  containers: Container[]
  online: boolean
}

const props = defineProps<Props>()

const selectedId = ref('')
const lines = ref<string[]>([])
const loading = ref(false)
const errorText = ref('')

/** 请求序号：快速切换容器时丢弃迟到的旧响应 */
let seq = 0

async function load(): Promise<void> {
  const agentId = props.agentId, containerId = selectedId.value
  if (containerId === '' || agentId === '' || !props.online) return
  const mine = ++seq
  loading.value = true
  errorText.value = ''
  try {
    const res = await fetchContainerLogs(agentId, containerId, 200)
    if (mine !== seq) return
    lines.value = res.lines
  } catch (err) {
    if (mine !== seq) return
    errorText.value = err instanceof Error ? err.message : '日志加载失败'
    lines.value = []
  } finally {
    if (mine === seq) loading.value = false
  }
}

watch(() => [selectedId.value, props.agentId, props.online] as const, ([id]) => {
  // 换容器先清空，避免短暂显示上一个容器的日志造成误读
  seq++
  lines.value = []
  errorText.value = ''
  loading.value = false
  if (id !== '') void load()
})
watch(() => props.containers, (containers) => {
  if (!containers.some(c => c.id === selectedId.value)) selectedId.value = ''
})
onUnmounted(() => { seq++; lines.value = [] })
</script>

<template>
  <div class="ext-logs">
    <div class="ext-logs__head">
      <select v-model="selectedId" class="ext-logs__select" aria-label="选择日志容器">
        <option value="">选择容器</option>
        <option v-for="c in containers" :key="c.id" :value="c.id">
          {{ c.name === '' ? c.id : c.name }}
        </option>
      </select>
      <n-button size="small" :disabled="selectedId === '' || !online" :loading="loading" @click="load">
        刷新日志
      </n-button>
    </div>

    <p v-if="!online" class="ext-logs__hint">服务器离线，无法读取日志。</p>
    <p v-else-if="selectedId === ''" class="ext-logs__hint">请先选择一个容器。</p>
    <p v-else-if="errorText !== ''" class="ext-logs__error">{{ errorText }}</p>
    <pre v-else class="ext-logs__box">{{ lines.join('\n') }}</pre>
  </div>
</template>

<style scoped>
.ext-logs {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.ext-logs__head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.ext-logs__select {
  padding: 4px 8px;
  border: 1px solid var(--arkade-border-color);
  border-radius: 4px;
  background: var(--arkade-page-bg);
  color: var(--arkade-text-primary);
  max-width: 260px;
}

.ext-logs__box {
  margin: 0;
  padding: 10px;
  max-height: 420px;
  overflow: auto;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
  border-radius: 4px;
}

.ext-logs__hint {
  margin: 0;
  font-size: 13px;
  color: var(--arkade-text-tertiary);
}

.ext-logs__error {
  margin: 0;
  font-size: 13px;
  color: var(--arkade-error-color, #d03050);
}
</style>
