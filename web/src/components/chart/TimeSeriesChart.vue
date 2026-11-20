<script setup lang="ts">
/**
 * 手写的 SVG 时间序列折线图。
 *
 * ## 为什么不引图表库
 * 全站只这一处要画时序线，echarts 打包接近 1MB；这里的需求只有
 * 「折线 + 悬停读数 + 拖拽缩放」，用 SVG 加几十行坐标换算就够，也少一个依赖。
 *
 * ## 三条硬约定
 * 1. **空值必须断线**：`null` 表示这一桶没有可用样本，不能连成直线更不能补 0，
 *    否则"没有数据"会被读成"用量为 0"。
 * 2. **横轴按真实时间定位**：样本稀疏时留出空洞，不把点等距摊开，
 *    否则一条"每 5 分钟一个点"的曲线看起来会和密集采样一样平滑。
 * 3. **缩放交给父组件**：图表只上报选中的时间区间，由父组件重新查询，
 *    这样缩放后的数据仍然是服务端降采样过的，不会放大出一堆假精度。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

export interface ChartSeries {
  label: string
  color: string
  /** 与 points 等长；null 表示该点无数据。 */
  values: (number | null)[]
  /** 用于上限、参考线这类辅助线。 */
  dashed?: boolean
}

const props = withDefaults(
  defineProps<{
    /** 各点的 Unix 秒级时间戳，升序。 */
    points: number[]
    series: ChartSeries[]
    /** 查询区间，用于横轴刻度。 */
    from: number
    to: number
    height?: number
    /** 固定纵轴上限（百分比图用 100）；0 表示按数据自动取整。 */
    yMax?: number
    /** 数值格式化，默认原样输出。 */
    format?: (value: number) => string
    emptyText?: string
  }>(),
  {
    height: 190,
    yMax: 0,
    // 默认值必须写成字面量函数，withDefaults 不接受外部引用
    format: (value: number) => String(value),
    emptyText: '这段时间没有数据'
  }
)

const emit = defineEmits<{ zoom: [from: number, to: number] }>()

// 渐变 id 必须全局唯一，否则多个图共用同一个 <defs> id 会串色
let chartSeq = 0
const gradientId = `ts-chart-${++chartSeq}`

const PAD = { top: 12, right: 14, bottom: 24, left: 56 }
const wrap = ref<HTMLDivElement | null>(null)
const width = ref(720)
let observer: ResizeObserver | null = null

onMounted(() => {
  if (wrap.value === null) return
  // 只在宽度真的变化时才写 ref，避免拖拽窗口时反复触发整图重算
  observer = new ResizeObserver((entries) => {
    const next = Math.floor(entries[0]?.contentRect.width ?? 0)
    if (next > 0 && next !== width.value) width.value = next
  })
  observer.observe(wrap.value)
  const measured = Math.floor(wrap.value.getBoundingClientRect().width)
  if (measured > 0) width.value = measured
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
})

const plotWidth = computed(() => Math.max(40, width.value - PAD.left - PAD.right))
const plotHeight = computed(() => Math.max(40, props.height - PAD.top - PAD.bottom))
const showEmpty = computed(() => props.points.length === 0)

/** 把数值向上取整到 1/2/5×10ⁿ，避免纵轴出现 837% 这种刻度。 */
function niceCeil(value: number): number {
  if (!Number.isFinite(value) || value <= 0) return 1
  const exponent = Math.floor(Math.log10(value))
  const base = 10 ** exponent
  const ratio = value / base
  const step = ratio <= 1 ? 1 : ratio <= 2 ? 2 : ratio <= 5 ? 5 : 10
  return step * base
}

const maxValue = computed(() => {
  if (props.yMax > 0) return props.yMax
  let max = 0
  for (const series of props.series) {
    for (const value of series.values) {
      if (value !== null && Number.isFinite(value) && value > max) max = value
    }
  }
  return niceCeil(max)
})

const yTicks = computed(() => {
  const max = maxValue.value
  return [0, max / 2, max].map((value) => ({
    value,
    y: PAD.top + plotHeight.value * (1 - value / max),
    label: props.format(value)
  }))
})

function xAt(at: number): number {
  const span = props.to - props.from
  if (span <= 0) return PAD.left
  const ratio = Math.min(1, Math.max(0, (at - props.from) / span))
  return PAD.left + ratio * plotWidth.value
}

function yAt(value: number): number {
  const max = maxValue.value || 1
  const ratio = Math.min(1, Math.max(0, value / max))
  return PAD.top + plotHeight.value * (1 - ratio)
}

/** 横轴刻度：4 段；跨度大时带上日期，否则只显示时分。 */
const xTicks = computed(() => {
  const span = props.to - props.from
  const count = 4
  const out: { x: number; label: string }[] = []
  for (let i = 0; i <= count; i++) {
    const at = props.from + (span * i) / count
    out.push({ x: xAt(at), label: axisTime(at, span) })
  }
  return out
})

function pad2(value: number): string {
  return String(value).padStart(2, '0')
}

function axisTime(seconds: number, span: number): string {
  const date = new Date(seconds * 1000)
  if (!Number.isFinite(date.getTime())) return ''
  const hm = `${pad2(date.getHours())}:${pad2(date.getMinutes())}`
  if (span <= 30 * 3600) return hm
  return `${pad2(date.getMonth() + 1)}-${pad2(date.getDate())} ${hm}`
}

function fullTime(seconds: number): string {
  const date = new Date(seconds * 1000)
  if (!Number.isFinite(date.getTime())) return '—'
  return (
    `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())} ` +
    `${pad2(date.getHours())}:${pad2(date.getMinutes())}`
  )
}

/** 逐点拼接路径，遇到 null 断开，形成多段子路径。 */
function linePath(values: (number | null)[]): string {
  let d = ''
  let pen = false
  for (let i = 0; i < props.points.length; i++) {
    const value = values[i]
    if (value === null || value === undefined || !Number.isFinite(value)) {
      pen = false
      continue
    }
    const x = xAt(props.points[i]).toFixed(1)
    const y = yAt(value).toFixed(1)
    d += `${pen ? ' L' : ' M'}${x},${y}`
    pen = true
  }
  return d.trim()
}

/** 单序列时补一块渐变面积，多序列不铺面积（否则互相遮挡看不清）。 */
const areaPath = computed(() => {
  if (props.series.length !== 1) return ''
  const values = props.series[0].values
  let first = -1
  let last = -1
  let d = ''
  let pen = false
  for (let i = 0; i < props.points.length; i++) {
    const value = values[i]
    if (value === null || value === undefined || !Number.isFinite(value)) {
      pen = false
      continue
    }
    if (first < 0) first = i
    last = i
    const x = xAt(props.points[i]).toFixed(1)
    const y = yAt(value).toFixed(1)
    d += `${pen ? ' L' : ' M'}${x},${y}`
    pen = true
  }
  if (d === '' || first < 0 || last < 0) return ''
  const baseY = PAD.top + plotHeight.value
  const startX = xAt(props.points[first]).toFixed(1)
  const endX = xAt(props.points[last]).toFixed(1)
  return `${d} L${endX},${baseY} L${startX},${baseY} Z`
})

// ============ 悬停读数 + 拖拽缩放 ============

const hoverIndex = ref<number | null>(null)
const dragStart = ref<number | null>(null)
const dragEnd = ref<number | null>(null)

const dragging = computed(() => dragStart.value !== null && dragEnd.value !== null)

function pointerX(event: PointerEvent): number {
  const target = event.currentTarget as SVGRectElement
  const rect = target.getBoundingClientRect()
  return Math.min(plotWidth.value, Math.max(0, event.clientX - rect.left))
}

function xToTime(x: number): number {
  const span = props.to - props.from
  return props.from + (x / plotWidth.value) * span
}

function nearestIndex(x: number): number | null {
  if (props.points.length === 0) return null
  let best = 0
  let bestDistance = Number.POSITIVE_INFINITY
  for (let i = 0; i < props.points.length; i++) {
    const distance = Math.abs(xAt(props.points[i]) - PAD.left - x)
    if (distance < bestDistance) {
      bestDistance = distance
      best = i
    }
  }
  // 离最近点太远就不吸附，避免鼠标在空白处也弹出一个读数
  return bestDistance <= 24 ? best : null
}

function onPointerDown(event: PointerEvent): void {
  const x = pointerX(event)
  dragStart.value = x
  dragEnd.value = x
  hoverIndex.value = null
  ;(event.currentTarget as SVGRectElement).setPointerCapture(event.pointerId)
}

function onPointerMove(event: PointerEvent): void {
  const x = pointerX(event)
  if (dragStart.value !== null) {
    dragEnd.value = x
    return
  }
  hoverIndex.value = nearestIndex(x)
}

function onPointerUp(event: PointerEvent): void {
  const start = dragStart.value
  const end = dragEnd.value
  dragStart.value = null
  dragEnd.value = null
  if (start === null || end === null) return
  // 小于 12px 视为误触/点击，不改变查询范围
  if (Math.abs(end - start) < 12) {
    hoverIndex.value = nearestIndex(pointerX(event))
    return
  }
  const from = Math.round(xToTime(Math.min(start, end)))
  const to = Math.round(xToTime(Math.max(start, end)))
  if (to - from >= 60) emit('zoom', from, to)
}

function onPointerLeave(): void {
  hoverIndex.value = null
  if (dragging.value) {
    dragStart.value = null
    dragEnd.value = null
  }
}

const hoverX = computed(() => {
  const index = hoverIndex.value
  return index === null ? 0 : xAt(props.points[index])
})

const tooltipStyle = computed(() => {
  const maxLeft = Math.max(0, width.value - 150)
  return {
    left: `${Math.min(maxLeft, Math.max(8, hoverX.value - 70))}px`,
    top: `${PAD.top + 4}px`
  }
})
</script>

<template>
  <div ref="wrap" class="ts-chart" :style="{ height: `${height}px` }">
    <p v-if="showEmpty" class="ts-chart__empty">{{ emptyText }}</p>
    <svg
      v-else
      class="ts-chart__svg"
      :width="width"
      :height="height"
      :viewBox="`0 0 ${width} ${height}`"
      role="img"
    >
      <defs>
        <linearGradient :id="gradientId" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" :stop-color="series[0]?.color ?? '#2080f0'" stop-opacity="0.18" />
          <stop offset="100%" :stop-color="series[0]?.color ?? '#2080f0'" stop-opacity="0.01" />
        </linearGradient>
      </defs>

      <!-- 网格与纵轴刻度 -->
      <g class="ts-chart__grid">
        <line
          v-for="tick in yTicks"
          :key="`y-${tick.value}`"
          :x1="PAD.left"
          :x2="PAD.left + plotWidth"
          :y1="tick.y"
          :y2="tick.y"
        />
      </g>
      <g class="ts-chart__axis">
        <text
          v-for="tick in yTicks"
          :key="`yl-${tick.value}`"
          :x="PAD.left - 8"
          :y="tick.y + 4"
          text-anchor="end"
        >
          {{ tick.label }}
        </text>
        <text
          v-for="tick in xTicks"
          :key="`xl-${tick.x}`"
          :x="tick.x"
          :y="height - 6"
          :text-anchor="tick.x === PAD.left ? 'start' : tick.x >= width - PAD.right - 1 ? 'end' : 'middle'"
        >
          {{ tick.label }}
        </text>
      </g>

      <path v-if="areaPath !== ''" :d="areaPath" :fill="`url(#${gradientId})`" stroke="none" />
      <path
        v-for="series in series"
        :key="series.label"
        class="ts-chart__line"
        :d="linePath(series.values)"
        :stroke="series.color"
        :stroke-dasharray="series.dashed === true ? '5 4' : undefined"
        fill="none"
      />

      <!-- 拖拽选中的时间区间 -->
      <rect
        v-if="dragging"
        class="ts-chart__selection"
        :x="PAD.left + Math.min(dragStart ?? 0, dragEnd ?? 0)"
        :y="PAD.top"
        :width="Math.abs((dragEnd ?? 0) - (dragStart ?? 0))"
        :height="plotHeight"
      />

      <!-- 悬停十字线与读数点 -->
      <g v-if="hoverIndex !== null && !dragging">
        <line
          class="ts-chart__crosshair"
          :x1="hoverX"
          :x2="hoverX"
          :y1="PAD.top"
          :y2="PAD.top + plotHeight"
        />
        <circle
          v-for="series in series"
          :key="`dot-${series.label}`"
          :cx="hoverX"
          :cy="series.values[hoverIndex] === null || series.values[hoverIndex] === undefined
            ? -10
            : yAt(series.values[hoverIndex] as number)"
          r="3"
          :fill="series.color"
        />
      </g>

      <!-- 透明接收层放在最后，保证所有指针事件都能拿到 -->
      <rect
        class="ts-chart__capture"
        :x="PAD.left"
        :y="PAD.top"
        :width="plotWidth"
        :height="plotHeight"
        fill="transparent"
        @pointerdown="onPointerDown"
        @pointermove="onPointerMove"
        @pointerup="onPointerUp"
        @pointerleave="onPointerLeave"
        @pointercancel="onPointerLeave"
      />
    </svg>

    <div v-if="hoverIndex !== null && !dragging" class="ts-chart__tooltip" :style="tooltipStyle">
      <p class="ts-chart__tooltip-time">{{ fullTime(points[hoverIndex]) }}</p>
      <p v-for="series in series" :key="`tip-${series.label}`" class="ts-chart__tooltip-row">
        <span class="ts-chart__dot" :style="{ background: series.color }" />
        <span class="ts-chart__tooltip-label">{{ series.label }}</span>
        <strong>{{
          series.values[hoverIndex] === null || series.values[hoverIndex] === undefined
            ? '—'
            : format(series.values[hoverIndex] as number)
        }}</strong>
      </p>
    </div>
  </div>
</template>

<style scoped>
.ts-chart {
  position: relative;
  width: 100%;
  user-select: none;
}

.ts-chart__svg {
  display: block;
  touch-action: none;
}

.ts-chart__empty {
  margin: 0;
  padding: 56px 0;
  text-align: center;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

.ts-chart__grid line {
  stroke: var(--arkade-border-color);
  stroke-width: 1;
}

.ts-chart__axis text {
  font-size: 11px;
  fill: var(--arkade-text-tertiary);
  font-variant-numeric: tabular-nums;
}

.ts-chart__line {
  stroke-width: 1.6;
  stroke-linejoin: round;
  stroke-linecap: round;
}

.ts-chart__selection {
  fill: var(--arkade-primary);
  fill-opacity: 0.12;
  stroke: var(--arkade-primary);
  stroke-opacity: 0.4;
}

.ts-chart__crosshair {
  stroke: var(--arkade-border-strong);
  stroke-dasharray: 3 3;
}

.ts-chart__capture {
  cursor: crosshair;
}

.ts-chart__tooltip {
  position: absolute;
  min-width: 132px;
  padding: 8px 10px;
  border: 1px solid var(--arkade-border-color);
  border-radius: 8px;
  background: var(--arkade-surface-bg);
  box-shadow: 0 6px 18px rgb(0 0 0 / 10%);
  pointer-events: none;
  font-size: 12px;
}

.ts-chart__tooltip-time {
  margin: 0 0 6px;
  color: var(--arkade-text-tertiary);
  font-variant-numeric: tabular-nums;
}

.ts-chart__tooltip-row {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 2px 0;
}

.ts-chart__tooltip-label {
  flex: 1;
  color: var(--arkade-text-secondary);
}

.ts-chart__tooltip-row strong {
  font-variant-numeric: tabular-nums;
}

.ts-chart__dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex: none;
}
</style>
