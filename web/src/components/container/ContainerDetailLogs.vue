<script setup lang="ts">
/**
 * 容器详情页的日志面板
 *
 * 与列表页的抽屉不同，这里是页面内的常驻面板。
 * 之所以单独写而不复用抽屉：抽屉是「弹出来看一眼」的交互，
 * 而详情页里日志是主内容之一，需要一直可见。
 *
 * 复用同一个接口 `GET .../logs?tail=N`（**view 权限**），
 * 所以即使详情 403，这个面板仍然正常工作。
 */
import { nextTick, onMounted, ref } from 'vue'
import { NButton, NSelect, NSwitch } from 'naive-ui'
import { fetchContainerLogs } from '@/api/containers'
import { isLogNearBottom, scrollLogToBottom } from '@/utils/logScroll'

interface Props {
  agentId: string
  containerId: string
  /** 面板标题里显示的名字，不影响请求 */
  name: string
}

const props = defineProps<Props>()

const TAIL_OPTIONS = [
  { label: '最近 100 行', value: 100 },
  { label: '最近 200 行', value: 200 },
  { label: '最近 500 行', value: 500 },
  { label: '最近 1000 行', value: 1000 }
]

const tail = ref(200)
const lines = ref<string[]>([])
const loading = ref(false)
const errorText = ref('')
const truncated = ref(false)

/**
 * 自动滚动开关，默认开启。
 *
 * 日志更新后默认贴到底部（最新一行在最下面），用户主动往上翻时自动关掉，
 * 免得下一轮刷新把阅读位置冲走；翻回底部附近再自动打开。
 * 用户也可以直接点开关手动控制。
 */
const autoScroll = ref(true)

const scrollRef = ref<HTMLElement | null>(null)

let seq = 0

/** 加载完成后按开关决定是否贴底 */
async function applyScroll(): Promise<void> {
  if (!autoScroll.value) return
  await nextTick()
  scrollLogToBottom(scrollRef.value)
}

/**
 * 滚动事件：用户往上翻就停止自动滚动，翻回底部附近再恢复。
 *
 * 不区分「用户滚动」和「程序滚动」：程序滚到底后本就处于底部附近，
 * 判定结果同样是保持开启，无需额外标志位。
 */
function handleScroll(): void {
  autoScroll.value = isLogNearBottom(scrollRef.value)
}

/** 手动切换开关：打开时立即贴底，符合「我要看最新」的预期 */
function handleToggleAutoScroll(): void {
  if (autoScroll.value) void applyScroll()
}

async function load(): Promise<void> {
  if (props.agentId === '' || props.containerId === '') return
  const mine = ++seq
  loading.value = true
  errorText.value = ''
  try {
    const res = await fetchContainerLogs(props.agentId, props.containerId, tail.value)
    if (mine !== seq) return
    lines.value = res.lines
    truncated.value = res.truncated
    await applyScroll()
  } catch (err) {
    if (mine !== seq) return
    errorText.value = err instanceof Error ? err.message : '日志加载失败'
    lines.value = []
  } finally {
    if (mine === seq) loading.value = false
  }
}

function handleTailChange(): void {
  void load()
}

onMounted(() => {
  void load()
})
</script>

<template>
  <div class="detail-logs">
    <div class="detail-logs__head">
      <n-select
        v-model:value="tail"
        :options="TAIL_OPTIONS"
        size="small"
        class="detail-logs__tail"
        @update:value="handleTailChange"
      />
      <n-button size="small" :loading="loading" @click="load">刷新</n-button>
      <label class="detail-logs__auto">
        <n-switch v-model:value="autoScroll" size="small" @update:value="handleToggleAutoScroll" />
        <span>自动滚动</span>
      </label>
      <span v-if="truncated" class="detail-logs__muted">内容已被截断，仅显示最近 {{ tail }} 行</span>
    </div>

    <p v-if="errorText !== ''" class="detail-logs__error">{{ errorText }}</p>
    <pre v-else-if="lines.length === 0 && !loading" class="detail-logs__empty">暂无日志输出。</pre>
    <pre v-else ref="scrollRef" class="detail-logs__box" @scroll="handleScroll">{{ lines.join('\n') }}</pre>
  </div>
</template>

<style scoped>
.detail-logs {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.detail-logs__head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.detail-logs__tail {
  width: 140px;
}

/* 自动滚动开关：和刷新按钮同排，不抢视觉重点 */
.detail-logs__auto {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--arkade-text-secondary);
  cursor: pointer;
  user-select: none;
}

.detail-logs__box,
.detail-logs__empty {
  margin: 0;
  padding: 10px;
  max-height: 460px;
  overflow: auto;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  white-space: pre-wrap;
  word-break: break-all;
  background: var(--arkade-page-bg);
  border: 1px solid var(--arkade-border-color);
  border-radius: 4px;
}

.detail-logs__empty {
  color: var(--arkade-text-tertiary);
}

.detail-logs__muted {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.detail-logs__error {
  margin: 0;
  font-size: 13px;
  color: var(--arkade-error-color, var(--arkade-danger));
}
</style>
