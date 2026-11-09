<script setup lang="ts">
/**
 * 容器日志抽屉（支持实时跟随）
 *
 * ## 为什么用轮询而不是 WebSocket / SSE
 *
 * 后端目前只提供一次性拉取的 `GET .../logs?tail=N` 接口，没有流式通道。
 * 前端能选的实现只有轮询，这是当前契约下唯一可行的方案。
 *
 * 后续如果要换成推送，需要改的地方集中在这里：
 * 1. 把 `usePolling` 换成 `EventSource`（SSE）或 WebSocket 连接；
 * 2. `handleIncoming` 的入参从「整个 tail 窗口」变成「增量行」，
 *    其余合并/去重/滚动逻辑不变；
 * 3. `connectionState` 的三个状态（连接中/已连接/异常）可以直接复用。
 * 也就是说，UI 与滚动逻辑与传输方式解耦，换推送时不必重写交互。
 *
 * ## 轮询策略：整段刷新有界快照，不做任何文本去重
 *
 * 后端 logs 接口只支持 `tail=N`（返回最近 N 行的**快照**），
 * 没有「从第 X 行开始」的增量语义，返回的每一行也不带序号或时间戳。
 *
 * 因此这里**直接把每轮的返回整体替换掉当前内容**，绝不做「按内容比对求增量」：
 * 日志里天然存在完全相同的行（重复的报错栈、心跳行、空行），
 * 任何基于文本的「识别新增部分」都会把合法的重复行当成已存在而丢掉，
 * 而丢掉日志比多刷一次内容严重得多——运维靠日志定位问题，看不全等于没有。
 *
 * 代价是每轮都重建一次文本节点；在有界窗口（默认 200 行）下开销可以忽略，
 * 且用户视觉上因为内容高度稳定，也不会出现跳动。
 *
 * 后续如果后端提供带序号/游标的增量接口，只需把 pull() 里的整体替换
 * 换成按游标追加，其余滚动与跟随逻辑不用动。
 *
 * ## 跟随（auto-follow）的关键行为
 *
 * 用户正在读某段报错时，新日志把他冲走是这个功能最典型的失败方式。
 * 因此：**只要用户手动向上滚动，就立刻停止跟随**，并显示「回到最新」按钮。
 * 判据是「距底部是否在阈值内」，而不是监听 scroll 方向——
 * 滚动到底部附近（用户想看最新）就恢复跟随，符合直觉且不需要猜意图。
 */
import { computed, nextTick, ref, watch } from 'vue'
import { NAlert, NButton, NDrawer, NDrawerContent, NIcon, NSpin, NSwitch } from 'naive-ui'
import AppIcon from '@/components/common/AppIcon.vue'
import { usePolling } from '@/composables/usePolling'
import { fetchContainerLogs } from '@/api/containers'
import { ApiError } from '@/types/api'
import type { Container } from '@/types/container'

interface Props {
  /** 是否显示抽屉（v-model:show） */
  show: boolean
  agentId: string
  /** 目标容器；为 null 时抽屉为空 */
  container: Container | null
  /** 拉取行数，契约默认 200 */
  tail?: number
}

const props = withDefaults(defineProps<Props>(), {
  tail: 200
})

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
}>()

/** 实时跟随的轮询间隔（毫秒）：2 秒足够「接近实时」，又不会给后端和浏览器太大压力 */
const FOLLOW_INTERVAL_MS = 2000

/**
 * 判定「是否在底部」的容差（像素）。
 *
 * 不能要求严格等于 scrollHeight：不同浏览器的滚动位置有舍入误差，
 * 且行高不是整数时永远差零点几像素，严格判断会导致跟随永久失效。
 */
const BOTTOM_THRESHOLD_PX = 24

/** 日志行 */
const lines = ref<string[]>([])
/** 是否被 tail 截断 */
const truncated = ref(false)
/** 首次加载中（只用于首屏，后续轮询不显示骨架，避免闪烁） */
const loading = ref(false)
/** 错误文案 */
const errorText = ref('')
/** 实时跟随时开关，默认开启 */
const followEnabled = ref(true)
/**
 * 是否跟随滚动到底部。
 *
 * 与 followEnabled 分开：followEnabled 管「要不要拉新日志」，
 * autoScroll 管「拉到后要不要滚下去」。用户往上翻时只关后者，拉取继续，
 * 这样用户回到底部时内容已经是最新的，不必等下一轮。
 */
const autoScroll = ref(true)

const scrollRef = ref<HTMLElement | null>(null)

const title = computed(() =>
  props.container ? `容器日志 · ${props.container.name}` : '容器日志'
)

/** 抽屉是否处于「可工作」状态：打开且有目标容器 */
const active = computed(() => props.show && props.container !== null)

/**
 * 拉取并刷新日志快照。
 *
 * 直接用后端返回整体替换，不做任何增量合并（原因见文件头注释）。
 *
 * @param first 是否首屏加载（决定要不要显示骨架）
 * @returns 内容是否发生了变化（供调用方决定是否需要滚动到底）
 */
async function pull(first: boolean): Promise<boolean> {
  const target = props.container
  if (!target) return false

  if (first) loading.value = true
  try {
    const res = await fetchContainerLogs(props.agentId, target.id, props.tail ?? 200)
    truncated.value = res.truncated
    // 内容真的变了才触发更新，避免每轮都让 Vue 重建文本节点
    const changed =
      res.lines.length !== lines.value.length ||
      res.lines.some((line, i) => line !== lines.value[i])
    if (changed) lines.value = res.lines
    errorText.value = ''
    return changed || first
  } finally {
    if (first) loading.value = false
  }
}

/**
 * 滚到底部。
 *
 * 用原生 scrollTop 而不是 NScrollbar 的 scrollTo：
 * 这里需要「跟随」语义——每次新内容到达都无条件滚到底，
 * 而 NScrollbar 的平滑滚动在 2 秒一轮的高频下会互相打断，观感很抖。
 */
async function scrollToBottom(): Promise<void> {
  await nextTick()
  const el = scrollRef.value
  if (el === null) return
  el.scrollTop = el.scrollHeight
}

/** 判断当前是否已在底部附近 */
function isNearBottom(): boolean {
  const el = scrollRef.value
  if (el === null) return true
  return el.scrollHeight - el.scrollTop - el.clientHeight <= BOTTOM_THRESHOLD_PX
}

/**
 * 滚动事件处理。
 *
 * 核心逻辑：用户手动往上翻 → 立即停止自动滚动（不再把他冲走）。
 * 翻回底部附近 → 自动恢复跟随，用户不必再点一次按钮。
 *
 * 注意这里没有区分「用户滚动」和「程序滚动」：程序滚到底部时 isNearBottom 为真，
 * 结果同样是「保持跟随」，因此不需要额外加标志位来防止自我触发。
 */
function handleScroll(): void {
  if (isNearBottom()) {
    autoScroll.value = true
  } else {
    autoScroll.value = false
  }
}

/** 「回到最新」按钮：滚到底并恢复跟随 */
async function handleBackToLatest(): Promise<void> {
  autoScroll.value = true
  await scrollToBottom()
  // 用户点了按钮说明想看最新，如果刚才因为关掉实时而没在拉，顺手打开
  if (!followEnabled.value) followEnabled.value = true
}

/** 手动刷新（保留原有能力，方便不想开实时的用户单次拉取） */
async function handleRefresh(): Promise<void> {
  const hasNew = await pull(false)
  if (hasNew || autoScroll.value) await scrollToBottom()
}

function handleClose(): void {
  emit('update:show', false)
}

/**
 * 实时跟随的轮询。
 *
 * 条件：抽屉打开 + 有目标容器 + 用户开启了实时 + 全局自动刷新开启。
 * 全局开关的联动在 usePolling 的 enabled 里表达，这里只管前三个。
 *
 * backoff: false —— 日志跟随失败时用户就盯着屏幕，
 * 退避会让「重连变慢」，与这里的体验目标相悖；
 * 连续失败仍会通过 failureCount 驱动界面的「连接异常」提示。
 */
const { failureCount, active: pollingActive } = usePolling(
  async () => {
    const hasNew = await pull(false)
    // 只有跟随打开且用户没往上翻时才自动滚动
    if (hasNew && autoScroll.value) await scrollToBottom()
  },
  {
    name: 'container-logs',
    interval: FOLLOW_INTERVAL_MS,
    enabled: computed(() => active.value && followEnabled.value),
    // 首屏由下面的 watch 负责，这里不重复拉
    immediate: false,
    backoff: false
  }
)

/** 是否正在异常重试中 */
const retrying = computed(() => failureCount.value > 0)

/**
 * 打开/切换容器时重置状态并加载首屏。
 *
 * 关闭时清空所有内容，避免下次打开闪现上一个容器的日志。
 */
watch(
  () => [props.show, props.container?.id] as const,
  async ([show]) => {
    if (!show) {
      lines.value = []
      errorText.value = ''
      truncated.value = false
      autoScroll.value = true
      return
    }
    lines.value = []
    autoScroll.value = true
    try {
      await pull(true)
      await scrollToBottom()
    } catch (err) {
      errorText.value = err instanceof ApiError ? err.message : '日志加载失败'
    }
  },
  { immediate: true }
)
</script>

<template>
  <n-drawer :show="show" :width="720" placement="right" @update:show="handleClose">
    <n-drawer-content :title="title" closable>
      <template #header>
        <div class="log-header">
          <span class="log-header__title">{{ title }}</span>
          <!-- 截断提示：让用户知道当前看到的不是完整日志 -->
          <span v-if="truncated" class="log-header__truncated">仅显示最近 {{ tail }} 行</span>
        </div>
      </template>

      <template #footer>
        <div class="log-footer">
          <div class="log-footer__left">
            <span class="log-footer__follow-label">实时跟随</span>
            <n-switch v-model:value="followEnabled" size="small" />
            <span v-if="pollingActive" class="log-footer__status">
              <span class="log-footer__dot log-footer__dot--on" />
              每 2 秒刷新最近 {{ tail }} 行
            </span>
            <span v-else-if="!followEnabled" class="log-footer__status">已暂停</span>
          </div>
          <n-button quaternary size="small" :loading="loading" @click="handleRefresh">刷新</n-button>
          <n-button size="small" @click="handleClose">关闭</n-button>
        </div>
      </template>

      <div class="log-body">
        <!--
          连接异常提示：不停止轮询、不清空已有日志。
          用户最容易误解成「日志停在这里了」，必须明确告诉他是在重试。
        -->
        <n-alert v-if="retrying" type="warning" :bordered="false" class="log-body__alert">
          连接异常，正在重试（已重试 {{ failureCount }} 次）。
        </n-alert>

        <n-spin :show="loading && lines.length === 0">
          <div v-if="errorText !== ''" class="log-error">{{ errorText }}</div>
          <div v-else class="log-scroll-wrap">
            <!--
              用原生 div 而不是 NScrollbar：需要精确读取/设置 scrollTop/scrollHeight/clientHeight
              来实现「跟随到底部」与「判断用户是否翻走了」，NScrollbar 封装了这层不易控制。
            -->
            <div ref="scrollRef" class="log-scroll" @scroll.passive="handleScroll">
              <pre v-if="lines.length > 0" class="log-pre"><code>{{ lines.join('\n') }}</code></pre>
              <div v-else class="log-empty">暂无日志输出</div>
            </div>

            <!--
              「回到最新」按钮：仅当用户往上翻走（不再跟随）时出现。
              浮动在右下角，不占日志区空间，也不打断阅读。
            -->
            <transition name="log-back">
              <button
                v-if="!autoScroll && lines.length > 0"
                type="button"
                class="log-back"
                @click="handleBackToLatest"
              >
                <n-icon :size="14"><app-icon name="chevron-right" /></n-icon>
                <span>回到最新</span>
              </button>
            </transition>
          </div>
        </n-spin>
      </div>
    </n-drawer-content>
  </n-drawer>
</template>

<style scoped>
.log-header {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.log-header__title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* 截断标记：淡黄底，不用 n-tag 是为了避免自定义 header 挤压标题宽度 */
.log-header__truncated {
  flex-shrink: 0;
  padding: 1px 6px;
  font-size: 12px;
  font-weight: normal;
  color: #b25e00;
  background: #fff7e6;
  border-radius: 4px;
}

.log-body {
  height: 100%;
  display: flex;
  flex-direction: column;
  min-height: 0;
}

.log-body__alert {
  margin-bottom: 8px;
  flex-shrink: 0;
}

/* 日志区外层：定位基准，供「回到最新」按钮绝对定位 */
.log-scroll-wrap {
  position: relative;
  flex: 1;
  min-height: 0;
}

/* 抽屉内容区撑满，滚动发生在日志内部而不是整页 */
.log-scroll {
  height: calc(100vh - 220px);
  overflow: auto;
  background: #1e2127;
  border-radius: 6px;
  /* 关键：min-width: 0 才能让内部超长行由自己滚动，而不是撑破抽屉 */
  min-width: 0;
}

.log-pre {
  margin: 0;
  padding: 12px;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.6;
  /* pre-wrap：长行自动折行，避免必须左右拖动才能读日志 */
  white-space: pre-wrap;
  word-break: break-all;
  color: #d8dee9;
}

.log-empty {
  padding: 32px;
  text-align: center;
  color: #8b9099;
}

.log-error {
  padding: 16px;
  color: #d03050;
  background: #fff1f0;
  border-radius: 6px;
}

/* 「回到最新」浮动按钮：深色底与日志区协调，又不至于看不见 */
.log-back {
  position: absolute;
  right: 16px;
  bottom: 16px;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 6px 12px;
  font-size: 12px;
  color: #fff;
  background: rgba(32, 128, 240, 0.92);
  border: none;
  border-radius: 16px;
  cursor: pointer;
  box-shadow: 0 2px 8px rgba(0, 0, 0, 0.25);
  transition: background 0.15s ease;
}

.log-back:hover {
  background: rgba(32, 128, 240, 1);
}

/* 图标做向下箭头用：chevron-right 旋转 90°，避免为一次性需求再加一个图标名 */
.log-back :deep(svg) {
  transform: rotate(90deg);
}

.log-back-enter-active,
.log-back-leave-active {
  transition: opacity 0.15s ease, transform 0.15s ease;
}

.log-back-enter-from,
.log-back-leave-to {
  opacity: 0;
  transform: translateY(4px);
}

.log-footer {
  display: flex;
  align-items: center;
  gap: 8px;
}

.log-footer__left {
  display: flex;
  align-items: center;
  gap: 8px;
  flex: 1;
  min-width: 0;
}

.log-footer__follow-label {
  font-size: 12px;
  color: #5c6169;
  white-space: nowrap;
}

.log-footer__status {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: #8b9099;
  white-space: nowrap;
}

/* 实时运行中的小绿点：一眼确认「现在确实在更新」，比纯文字更直观 */
.log-footer__dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: #18a058;
}

.log-footer__dot--on {
  animation: log-dot-pulse 2s ease-in-out infinite;
}

@keyframes log-dot-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.35;
  }
}

/* 手机端：日志区高度按视口减去顶栏与底部操作条 */
@media (max-width: 767.98px) {
  .log-scroll {
    height: calc(100vh - 260px);
  }
}
</style>
