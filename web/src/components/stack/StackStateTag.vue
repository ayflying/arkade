<script setup lang="ts">
/**
 * 编排状态标签
 *
 * 与容器状态标签（ContainerStateTag）分开的原因：
 * 编排是「一组服务」的整体状态，语义与单容器不同，
 * 尤其 `partial`（部分运行）在容器那里不存在。
 */
import { computed } from 'vue'
import { NTag } from 'naive-ui'

interface Props {
  /**
   * 状态值。
   *
   * 声明为 string 而不是 StackState：除了纳管编排的已知状态，
   * 还要用于展示服务器上**外部 compose 项目**的状态
   * （后端给的是 compose 原始状态文本，取值集合不保证与 StackState 一致）。
   * 下方 STATE_MAP 自带兜底，未知值原样展示。
   */
  state: string
}

const props = defineProps<Props>()

type TagType = 'success' | 'warning' | 'error' | 'default' | 'info'

interface StateMeta {
  label: string
  type: TagType
}

/**
 * 状态 → 文案 + 颜色。
 *
 * 用 Record + 兜底而不是 switch：后端将来加了状态值也不会渲染成空白，
 * 未知状态退化成原值，不会崩。
 *
 * offline 单独一类：Agent 离线时后端把每条编排都标成 offline，
 * 这不是「编排停止」，而是「查不到真实状态」，必须区分开，
 * 否则用户会以为自己的服务全被停了。
 */
const STATE_MAP: Record<string, StateMeta> = {
  running: { label: '运行中', type: 'success' },
  stopped: { label: '已停止', type: 'default' },
  // 部分运行是关键告警状态：说明只有部分服务起来了，需要用户介入
  partial: { label: '部分运行', type: 'warning' },
  offline: { label: '离线', type: 'info' },
  /*
   * 操作中：Agent 正在执行该项目的某个操作，此刻读到的状态是半写状态。
   * 必须是独立的第三态——归进「运行中」会让用户以为一切正常，
   * 归进「已停止」会让他去点启动从而与进行中的操作打架。
   * 用 warning 色提示「稍等，正在处理」。
   */
  busy: { label: '操作中', type: 'warning' },
  unknown: { label: '未知', type: 'info' }
}

const meta = computed<StateMeta>(() => STATE_MAP[props.state] ?? { label: props.state, type: 'info' })
</script>

<template>
  <n-tag size="small" :bordered="false" :type="meta.type">{{ meta.label }}</n-tag>
</template>
