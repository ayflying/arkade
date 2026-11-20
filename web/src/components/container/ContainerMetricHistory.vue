<script setup lang="ts">
/**
 * 容器资源历史面板（容器详情页的「监控」标签页）。
 *
 * ## 为什么历史需要单独的标签页
 * 概览里的「资源用量」是当前值，5 秒刷新一次；历史曲线回答的是另一个问题
 * （"昨天这个时候是不是也这么高"），混在一起会让人误以为曲线也是实时值。
 *
 * ## 数据从哪里来
 * Hub 把每分钟的采集结果按分钟桶落库，保留 7 天。查询区间由服务端夹紧，
 * 并按跨度降采样（点数上限 360），所以这里不做任何本地抽样。
 *
 * ## 空数据的两种原因，必须说清楚
 * 1. 这段时间确实没采到（监控刚开启、服务器离线）；
 * 2. 这个容器根本不在采集范围里——监控只采集**已接管的编排项目**下的容器，
 *    手动 `docker run` 起的容器永远不会有历史。
 * 用户看到空图时最想知道是哪一种，所以空状态文案把两种情况都写出来。
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { NAlert, NButton, NButtonGroup, NDatePicker, NSpin } from 'naive-ui'
import TimeSeriesChart from '@/components/chart/TimeSeriesChart.vue'
import type { ChartSeries } from '@/components/chart/TimeSeriesChart.vue'
import { getContainerMetricHistory } from '@/api/containerMetrics'
import type { ContainerMetricHistory, MetricHistoryRange } from '@/types/containerMetrics'
import { ApiError } from '@/types/api'
import { formatBytes } from '@/utils/formatSize'
import { formatAbsoluteTime } from '@/utils/formatTime'

const props = defineProps<{
  agentId: string
  containerId: string
}>()

const DAY = 86400
const RANGES: { value: MetricHistoryRange; label: string }[] = [
  { value: 'today', label: '今天' },
  { value: 'yesterday', label: '昨天' },
  { value: 'week', label: '最近七天' }
]

const range = ref<MetricHistoryRange>('today')
const customRange = ref<[number, number] | null>(null)
/** 拖拽缩放后的窗口；null 表示跟随预设。 */
const zoomWindow = ref<{ from: number; to: number } | null>(null)

const data = ref<ContainerMetricHistory | null>(null)
const loading = ref(false)
const errorText = ref('')
const hintText = ref('')

function startOfLocalDay(offsetDays = 0): number {
  const date = new Date()
  date.setHours(0, 0, 0, 0)
  date.setDate(date.getDate() + offsetDays)
  return Math.floor(date.getTime() / 1000)
}

/** 预设区间（Unix 秒）。 */
const presetWindow = computed<{ from: number; to: number }>(() => {
  const now = Math.floor(Date.now() / 1000)
  if (range.value === 'yesterday') {
    // 昨天整天：00:00 到今天 00:00。to 取"现在"会和今天重叠，所以卡在整点。
    return { from: startOfLocalDay(-1), to: startOfLocalDay(0) }
  }
  if (range.value === 'week') return { from: now - 7 * DAY, to: now }
  if (range.value === 'custom' && customRange.value !== null) {
    return {
      from: Math.floor(customRange.value[0] / 1000),
      to: Math.floor(customRange.value[1] / 1000)
    }
  }
  return { from: startOfLocalDay(0), to: now }
})

const viewWindow = computed(() => zoomWindow.value ?? presetWindow.value)

const points = computed<number[]>(() => (data.value?.points ?? []).map((point) => point.at))

function num(value: number | null | undefined): number | null {
  return typeof value === 'number' && Number.isFinite(value) ? value : null
}

const COLORS = {
  primary: '#2080f0',
  success: '#18a058',
  warning: '#f0a020',
  danger: '#d03050'
}

function seriesOf(
  label: string,
  color: string,
  pick: (index: number) => number | null,
  dashed = false
): ChartSeries {
  const list = data.value?.points ?? []
  return {
    label,
    color,
    dashed,
    values: list.map((_, index) => pick(index))
  }
}

const cpuSeries = computed<ChartSeries[]>(() => [
  seriesOf('CPU', COLORS.primary, (i) => num(data.value?.points[i]?.cpu_percent))
])

const memorySeries = computed<ChartSeries[]>(() => [
  seriesOf('使用', COLORS.success, (i) => num(data.value?.points[i]?.memory_bytes)),
  seriesOf('上限', COLORS.warning, (i) => num(data.value?.points[i]?.memory_limit_bytes), true)
])

const networkSeries = computed<ChartSeries[]>(() => [
  seriesOf('接收', COLORS.primary, (i) => num(data.value?.points[i]?.network_rx_per_second)),
  seriesOf('发送', COLORS.success, (i) => num(data.value?.points[i]?.network_tx_per_second))
])

const diskSeries = computed<ChartSeries[]>(() => [
  seriesOf('读', COLORS.primary, (i) => num(data.value?.points[i]?.block_read_per_second)),
  seriesOf('写', COLORS.warning, (i) => num(data.value?.points[i]?.block_write_per_second))
])

const hasPoints = computed(() => points.value.length > 0)

function percent(value: number): string {
  if (value > 0 && value < 0.01) return '<0.01%'
  return `${Number(value.toFixed(2))}%`
}

function bytes(value: number): string {
  return formatBytes(value)
}

function rate(value: number): string {
  if (value > 0 && value < 1) return '<1 B/s'
  return `${formatBytes(value)}/s`
}

function bucketLabel(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`
  return `${seconds} 秒`
}

const rangeText = computed(() => {
  const res = data.value
  if (res === null) return ''
  return `${formatAbsoluteTime(res.from)} — ${formatAbsoluteTime(res.to)}`
})

let seq = 0

async function load(): Promise<void> {
  if (props.agentId === '' || props.containerId === '') return
  const current = viewWindow.value
  if (current.to <= current.from) return
  const mine = ++seq
  loading.value = true
  errorText.value = ''
  try {
    const res = await getContainerMetricHistory(props.agentId, props.containerId, current.from, current.to)
    if (mine !== seq) return
    data.value = res
  } catch (err) {
    if (mine !== seq) return
    data.value = null
    if (err instanceof ApiError && err.status === 403) {
      errorText.value = '没有权限查看该服务器的资源历史'
    } else if (err instanceof ApiError) {
      errorText.value = err.message
    } else {
      errorText.value = '资源历史加载失败'
    }
  } finally {
    if (mine === seq) loading.value = false
  }
}

function selectRange(value: MetricHistoryRange): void {
  range.value = value
  if (value !== 'custom') customRange.value = null
}

function onCustomRange(value: [number, number] | null): void {
  if (value === null) {
    customRange.value = null
    hintText.value = ''
    if (range.value === 'custom') selectRange('today')
    return
  }
  const [from, to] = value
  // 超过保留期的区间就地夹紧并提示，不做静默截断
  if (to - from > 7 * DAY * 1000) {
    customRange.value = [to - 7 * DAY * 1000, to]
    hintText.value = '自定义时间最多跨 7 天，已自动截取结束时间之前的 7 天'
  } else {
    customRange.value = [from, to]
    hintText.value = ''
  }
  range.value = 'custom'
}

function onZoom(from: number, to: number): void {
  zoomWindow.value = { from, to }
  hintText.value = ''
}

function resetZoom(): void {
  zoomWindow.value = null
}

// 预设/自定义变化时清掉缩放窗口：否则用户切换"昨天"仍停留在旧的缩放区间里。
watch(presetWindow, () => {
  zoomWindow.value = null
})

watch(viewWindow, () => {
  void load()
}, { immediate: false })

watch([range, customRange], () => {
  void load()
})

// 「今天」的数据会持续增长，只在没有缩放时每分钟补一次；
// 缩放查看历史区间时不刷新，避免图形在用户眼前跳动。
let timer: number | null = null

function syncTimer(): void {
  if (timer !== null) {
    window.clearInterval(timer)
    timer = null
  }
  if (range.value === 'today' && zoomWindow.value === null) {
    timer = window.setInterval(() => void load(), 60_000)
  }
}

watch([range, zoomWindow], syncTimer)

onMounted(() => {
  void load()
  syncTimer()
})

onBeforeUnmount(() => {
  if (timer !== null) window.clearInterval(timer)
  timer = null
})

const showCustomPicker = computed(() => range.value === 'custom')
</script>

<template>
  <section class="metric-history">
    <div class="metric-history__toolbar">
      <n-button-group size="small">
        <n-button
          v-for="item in RANGES"
          :key="item.value"
          :type="range === item.value ? 'primary' : 'default'"
          :secondary="range !== item.value"
          @click="selectRange(item.value)"
        >
          {{ item.label }}
        </n-button>
        <n-button
          :type="range === 'custom' ? 'primary' : 'default'"
          :secondary="range !== 'custom'"
          @click="selectRange('custom')"
        >
          自定义时间
        </n-button>
      </n-button-group>

      <n-date-picker
        v-if="showCustomPicker"
        size="small"
        type="datetimerange"
        clearable
        :value="customRange"
        class="metric-history__picker"
        @update:value="onCustomRange"
      />

      <n-button v-if="zoomWindow !== null" size="small" quaternary @click="resetZoom">
        重置缩放
      </n-button>
      <n-button size="small" quaternary :loading="loading" @click="load">刷新</n-button>
    </div>

    <p v-if="rangeText !== ''" class="metric-history__summary">
      {{ rangeText }}
      <span v-if="data">· 每点 {{ bucketLabel(data.bucket_seconds) }} · 保留 {{ data.retention_days }} 天</span>
      <span v-if="zoomWindow !== null">· 已缩放</span>
    </p>

    <n-alert v-if="hintText !== ''" type="info" :bordered="false">{{ hintText }}</n-alert>
    <n-alert v-if="errorText !== ''" type="error" :bordered="false">{{ errorText }}</n-alert>

    <n-spin v-if="loading && data === null" size="small" />

    <n-alert v-else-if="data !== null && !hasPoints" type="info" :bordered="false" title="这段时间没有资源历史">
      监控只采集**已接管的编排项目**下的容器，手动用 docker run 启动的容器不会记录历史；如果是刚开启监控或服务器离线过，这段时间也会是空的。
    </n-alert>

    <div v-else-if="data !== null" class="metric-history__charts">
      <article class="metric-history__card">
        <header>
          <h4>CPU 使用率</h4>
          <span>单位：%</span>
        </header>
        <time-series-chart
          :points="points"
          :series="cpuSeries"
          :from="data.from"
          :to="data.to"
          :format="percent"
          @zoom="onZoom"
        />
      </article>

      <article class="metric-history__card">
        <header>
          <h4>内存</h4>
          <span>实线：已用 · 虚线：上限</span>
        </header>
        <time-series-chart
          :points="points"
          :series="memorySeries"
          :from="data.from"
          :to="data.to"
          :format="bytes"
          @zoom="onZoom"
        />
      </article>

      <article class="metric-history__card">
        <header>
          <h4>网络 IO</h4>
          <span>单位：字节/秒</span>
        </header>
        <time-series-chart
          :points="points"
          :series="networkSeries"
          :from="data.from"
          :to="data.to"
          :format="rate"
          @zoom="onZoom"
        />
      </article>

      <article class="metric-history__card">
        <header>
          <h4>磁盘 IO</h4>
          <span>单位：字节/秒</span>
        </header>
        <time-series-chart
          :points="points"
          :series="diskSeries"
          :from="data.from"
          :to="data.to"
          :format="rate"
          @zoom="onZoom"
        />
      </article>

      <p class="metric-history__tip">在任意图上左右拖拽可放大到该时间段；缩放后数据由服务端重新采样。</p>
    </div>
  </section>
</template>

<style scoped>
.metric-history {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.metric-history__toolbar {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
}

.metric-history__picker {
  width: 320px;
}

.metric-history__summary {
  margin: 0;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
  font-variant-numeric: tabular-nums;
}

.metric-history__charts {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(360px, 1fr));
  gap: 12px;
}

.metric-history__card {
  padding: 12px 14px 6px;
  border: 1px solid var(--arkade-border-color);
  border-radius: 10px;
  background: var(--arkade-surface-bg);
}

.metric-history__card header {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 4px;
}

.metric-history__card h4 {
  margin: 0;
  font-size: 13px;
  font-weight: 600;
}

.metric-history__card header span {
  font-size: 11px;
  color: var(--arkade-text-tertiary);
}

.metric-history__tip {
  grid-column: 1 / -1;
  margin: 0;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}
</style>
