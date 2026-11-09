<script setup lang="ts">
/**
 * 容器日志抽屉
 *
 * 交互设计：
 * - 用抽屉（右侧）而非对话框：日志是长文本，抽屉纵向空间更大，且不遮挡容器表格；
 * - 等宽字体 + 可滚动 + 自动滚到底部（最新日志在末尾，运维第一眼要看最新）；
 * - 顶部显示截断提示：后端因 tail 限制返回 truncated=true 时明确告知，避免误以为日志完整；
 * - 每次打开/切换容器都会重新拉取，关闭时不保留旧数据（避免串容器）。
 *
 * 为什么日志状态不放进全局 store：它只服务这一个抽屉，页面切换即弃，全局化反而增加失效成本。
 */
import { computed, ref, watch } from 'vue'
import { NButton, NDrawer, NDrawerContent, NScrollbar, NSpin } from 'naive-ui'
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
  // 默认拉取行数：与后端契约一致（tail=200），避免调用方漏传时退回过大的值
  tail: 200
})

const emit = defineEmits<{
  (e: 'update:show', value: boolean): void
}>()

/** 日志行 */
const lines = ref<string[]>([])
const loading = ref(false)
/** 是否被 tail 截断 */
const truncated = ref(false)
/** 加载失败文案；有值时用错误态替代日志区 */
const errorText = ref('')
/** 滚动容器引用，用于加载后自动滚到底部 */
const scrollRef = ref<InstanceType<typeof NScrollbar> | null>(null)

/** 抽屉标题文案 */
const title = computed(() =>
  props.container ? `容器日志 · ${props.container.name}` : '容器日志'
)

/**
 * 拉取日志。
 *
 * 边界：容器在日志加载过程中被删除/改名，后端返回 not_found，这里只展示错误文案，
 * 不抛到全局（用户可能只是想看一眼，不必弹窗打断）。
 */
async function loadLogs(): Promise<void> {
  const target = props.container
  if (!target) {
    lines.value = []
    return
  }
  loading.value = true
  errorText.value = ''
  truncated.value = false
  try {
    const res = await fetchContainerLogs(props.agentId, target.id, props.tail ?? 200)
    lines.value = res.lines
    truncated.value = res.truncated
    // 等 DOM 渲染完再滚动，否则 scrollHeight 还是旧值
    // NScrollbar 的 scrollTo 支持 { top, left, behavior }，这里直接滚到最大的 top 值
    requestAnimationFrame(() => {
      const scroller = scrollRef.value
      if (!scroller) return
      const viewport = scroller.$el as HTMLElement | undefined
      const maxTop = viewport ? viewport.scrollHeight : Number.MAX_SAFE_INTEGER
      scroller.scrollTo({ top: maxTop, behavior: 'auto' })
    })
  } catch (err) {
    lines.value = []
    errorText.value = err instanceof ApiError ? err.message : '日志加载失败'
  } finally {
    loading.value = false
  }
}

/** 手动刷新按钮 */
function handleRefresh(): void {
  void loadLogs()
}

/** 关闭抽屉：交给父组件控制显隐（受控组件，状态以父级为准） */
function handleClose(): void {
  emit('update:show', false)
}

// 抽屉打开且目标变化时重新拉取；关闭时清空，避免下次打开闪现上一个容器的日志
watch(
  () => [props.show, props.container?.id] as const,
  ([show]) => {
    if (show) {
      void loadLogs()
    } else {
      lines.value = []
      errorText.value = ''
      truncated.value = false
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
          <span>{{ title }}</span>
          <!-- 截断提示：让用户知道当前看到的不是完整日志 -->
          <span v-if="truncated" class="log-header__truncated">仅显示最近 {{ tail }} 行</span>
        </div>
      </template>

      <template #footer>
        <div class="log-footer">
          <n-button quaternary size="small" :loading="loading" @click="handleRefresh">刷新</n-button>
          <n-button size="small" @click="handleClose">关闭</n-button>
        </div>
      </template>

      <div class="log-body">
        <n-spin :show="loading">
          <div v-if="errorText !== ''" class="log-error">{{ errorText }}</div>
          <n-scrollbar v-else ref="scrollRef" class="log-scroll">
            <pre v-if="lines.length > 0" class="log-pre"><code>{{ lines.join('\n') }}</code></pre>
            <div v-else class="log-empty">暂无日志输出</div>
          </n-scrollbar>
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
}

/* 截断标记：淡黄底，不用 n-tag 是为了避免自定义 header 挤压标题宽度 */
.log-header__truncated {
  padding: 1px 6px;
  font-size: 12px;
  font-weight: normal;
  color: #b25e00;
  background: #fff7e6;
  border-radius: 4px;
}

.log-body {
  height: 100%;
}

/* 抽屉内容区撑满，日志滚动条限制在内部而不是整页滚动 */
.log-scroll {
  max-height: calc(100vh - 180px);
}

.log-pre {
  margin: 0;
  padding: 12px;
  font-family: var(--arkade-mono-font);
  font-size: 12px;
  line-height: 1.6;
  white-space: pre-wrap;
  word-break: break-all;
  color: #d8dee9;
  background: #1e2127;
  border-radius: 6px;
  min-height: 240px;
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

.log-footer {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
</style>
