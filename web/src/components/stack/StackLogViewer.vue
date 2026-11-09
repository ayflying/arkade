<script setup lang="ts">
/**
 * 编排日志查看
 *
 * 从详情页拆出的原因：日志区有自己的状态（选中服务、行数、加载、错误），
 * 塞进详情页会显著拉长主文件，也不利于单独调整。
 *
 * 交互：
 * - 顶部下拉切换服务（「全部服务」+ 各服务名）；
 * - 切换后立即重新拉取；
 * - 内容用等宽字体 + 自动滚到底部，方便盯最新日志。
 */
import { computed, nextTick, ref, watch } from 'vue'
import { NAlert, NButton, NSelect, NSwitch } from 'naive-ui'
import type { SelectOption } from 'naive-ui'
import { fetchStackLogs } from '@/api/stacks'
import { ApiError } from '@/types/api'

interface Props {
  agentId: string
  stackName: string
  /**
   * 可选服务名列表，用于下拉切换。
   * 详情接口的 services 是 `string[]`（只有名字），不是对象数组，
   * 因此这里直接用字符串。
   */
  services: string[]
  /** 服务器是否在线；离线时不请求 */
  online: boolean
  /** 是否可见（父组件用 tab 控制，不可见时不预加载） */
  visible: boolean
}

const props = defineProps<Props>()

const selectedService = ref<string>('')
const lines = ref<string[]>([])
const truncated = ref(false)
const loading = ref(false)
const errorText = ref('')
/** 拉取的行数；日志太长时用户可调小 */
const tail = ref(200)
/** 加载后是否自动滚到底部 */
const autoScroll = ref(true)

const contentRef = ref<HTMLElement | null>(null)

/** 服务下拉选项：第一项为「全部服务」 */
const serviceOptions = computed<SelectOption[]>(() => [
  { label: '全部服务', value: '' },
  ...props.services.map((name) => ({ label: name, value: name }))
])

/** 行数选项 */
const tailOptions: SelectOption[] = [
  { label: '200 行', value: 200 },
  { label: '500 行', value: 500 },
  { label: '1000 行', value: 1000 }
]

/**
 * 拉取日志。
 *
 * 边界：opened 为 false 时不请求（页面刚进来没必要拉）；
 * 服务器离线时直接给提示，不发必然失败的请求。
 */
async function load(): Promise<void> {
  if (!props.visible || !props.online) return
  loading.value = true
  errorText.value = ''
  try {
    const res = await fetchStackLogs(
      props.agentId,
      props.stackName,
      selectedService.value === '' ? undefined : selectedService.value,
      tail.value
    )
    lines.value = res.lines
    truncated.value = res.truncated
    if (autoScroll.value) {
      await nextTick()
      scrollToBottom()
    }
  } catch (err) {
    errorText.value = err instanceof ApiError ? err.message : '加载日志失败'
  } finally {
    loading.value = false
  }
}

/** 滚到日志底部：最新的日志通常在最下面 */
function scrollToBottom(): void {
  const el = contentRef.value
  if (el === null) return
  el.scrollTop = el.scrollHeight
}

/**
 * 切换服务 / 行数后重新拉取。
 *
 * 监听的是「值」而不是事件，这样无论是下拉改变还是父组件重置都能覆盖。
 * immediate 保证首次可见时也会加载。
 */
watch(
  [() => props.visible, selectedService, tail],
  () => {
    if (props.visible) void load()
  },
  { immediate: true }
)
</script>

<template>
  <div class="stack-logs">
    <div class="stack-logs__bar">
      <n-select
        v-model:value="selectedService"
        :options="serviceOptions"
        size="small"
        class="stack-logs__select"
        :disabled="loading"
      />
      <n-select
        v-model:value="tail"
        :options="tailOptions"
        size="small"
        class="stack-logs__tail"
        :disabled="loading"
      />
      <div class="stack-logs__auto">
        <span class="stack-logs__auto-label">自动滚动</span>
        <n-switch v-model:value="autoScroll" size="small" />
      </div>
      <div class="stack-logs__spacer" />
      <n-button size="small" :loading="loading" :disabled="!online" @click="load">刷新</n-button>
    </div>

    <n-alert v-if="!online" type="warning" :bordered="false" class="stack-logs__alert">
      服务器离线，无法获取日志。
    </n-alert>
    <n-alert v-else-if="errorText !== ''" type="error" :bordered="false" class="stack-logs__alert" closable>
      {{ errorText }}
    </n-alert>

    <!-- 日志内容：等宽字体、深色底，方便长时间阅读 -->
    <div ref="contentRef" class="stack-logs__content">
      <div v-if="loading && lines.length === 0" class="stack-logs__empty">正在加载日志…</div>
      <div v-else-if="lines.length === 0" class="stack-logs__empty">暂无日志输出</div>
      <pre v-else class="stack-logs__text">{{ lines.join('\n') }}</pre>
    </div>

    <!-- 被截断时明确告知，避免用户以为日志只有这么多 -->
    <div v-if="truncated" class="stack-logs__truncated">
      日志较长，仅显示末尾 {{ tail }} 行。可切换到更大的行数查看。
    </div>
  </div>
</template>

<style scoped>
.stack-logs__bar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  margin-bottom: 10px;
}

.stack-logs__select {
  width: 160px;
}

.stack-logs__tail {
  width: 110px;
}

.stack-logs__auto {
  display: flex;
  align-items: center;
  gap: 6px;
}

.stack-logs__auto-label {
  font-size: 12px;
  color: var(--arkade-text-tertiary);
  white-space: nowrap;
}

.stack-logs__spacer {
  flex: 1;
}

.stack-logs__alert {
  margin-bottom: 10px;
}

.stack-logs__content {
  max-height: 420px;
  overflow: auto;
  background: #1e2430;
  border-radius: 6px;
  padding: 12px;
  /* 关键：日志行很长时允许横向滚动，而不是撑破页面 */
  min-width: 0;
}

.stack-logs__text {
  margin: 0;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.6;
  color: #d6deeb;
  white-space: pre;
}

.stack-logs__empty {
  padding: 24px;
  text-align: center;
  font-size: 13px;
  color: #6b7788;
}

.stack-logs__truncated {
  margin-top: 8px;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}

/* 手机端下拉占满整行，避免挤成一团 */
@media (max-width: 767.98px) {
  .stack-logs__select,
  .stack-logs__tail {
    width: 100%;
  }
}
</style>
