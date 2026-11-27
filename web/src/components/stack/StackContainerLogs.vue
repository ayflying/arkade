<script setup lang="ts">
/**
 * 编排日志面板：默认汇总各容器的日志，复用现有的容器日志接口。
 */
import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { NButton } from 'naive-ui'
import { fetchContainerLogs } from '@/api/containers'
import type { Container } from '@/types/container'
import { readStackLogSnapshot } from './stackLogSnapshot'

interface Props {
  agentId: string
  containers: Container[]
  online: boolean
}

const props = defineProps<Props>()

const selectedId = ref('')
const targets = computed(() => selectedId.value === ''
  ? props.containers
  : props.containers.filter(c => c.id === selectedId.value))
const results = shallowRef<Awaited<ReturnType<typeof readStackLogSnapshot>>>([])
const loading = ref(false)
const failures = computed(() => results.value.filter(result => result.error !== undefined))
const truncated = computed(() => results.value.some(result => result.response?.truncated))
const logText = computed(() => results.value.flatMap(({ source, response }) => {
  if (!response) return []
  return selectedId.value === ''
    ? response.lines.map(line => `[${source.name || source.id}] ${line}`)
    : response.lines
}).join('\n'))

// 换目标、离线或离开标签时，丢弃迟到的结果并停止派发旧请求。
let seq = 0
async function load(): Promise<void> {
  const mine = ++seq
  const agentId = props.agentId
  results.value = []
  loading.value = Boolean(agentId && props.online && targets.value.length)
  if (!loading.value) return
  const snapshot = await readStackLogSnapshot(targets.value,
    id => fetchContainerLogs(agentId, id, 200), () => mine === seq)
  if (mine !== seq) return
  results.value = snapshot
  loading.value = false
}

watch(() => props.containers, containers => {
  if (selectedId.value && !containers.some(c => c.id === selectedId.value)) selectedId.value = ''
})
watch([selectedId, () => props.agentId, () => props.online, () => JSON.stringify(targets.value.map(c => c.id))],
  () => { void load() }, { immediate: true })
onScopeDispose(() => { seq++ })
</script>

<template>
  <div class="ext-logs">
    <div class="ext-logs__head">
      <select v-model="selectedId" class="ext-logs__select" aria-label="选择日志容器">
        <option value="">全部容器</option>
        <option v-for="c in containers" :key="c.id" :value="c.id">
          {{ c.name === '' ? c.id : c.name }}
        </option>
      </select>
      <n-button size="small" :disabled="!targets.length || !agentId || !online" :loading="loading" @click="load">
        刷新日志
      </n-button>
    </div>

    <p v-if="!online" class="ext-logs__hint">服务器离线，无法读取日志。</p>
    <p v-else-if="!targets.length" class="ext-logs__hint">没有可查看日志的容器。</p>
    <p v-else-if="loading" class="ext-logs__hint">正在读取日志…</p>
    <template v-else>
      <p v-for="{ source, error } in failures" :key="source.id" class="ext-logs__error">{{ source.name || source.id }}：{{ error }}</p>
      <p v-if="truncated" class="ext-logs__hint">部分日志输出已截断。</p>
      <pre v-if="logText" class="ext-logs__box">{{ logText }}</pre>
      <p v-else-if="failures.length < targets.length" class="ext-logs__hint">暂无日志。</p>
    </template>
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
  color: var(--arkade-error-color, var(--arkade-danger));
}
</style>
