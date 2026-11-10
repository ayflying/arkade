<script setup lang="ts">
/**
 * 自动接管状态标签（未纳管编排表用）
 *
 * ## 为什么单独成组件
 *
 * 编排列表已经是一个长视图，接管状态的「文案 + 配色 + 原因展示」有独立规则
 * （尤其原因必须一行截断且**不依赖 hover** 也能读到），抽出来既不撑长页面，
 * 也便于单独调整文案。
 *
 * ## `unknown` 一律不渲染
 *
 * `unknown` 是前端兜底值（旧 Hub 没有这个字段），**不是**「后端说暂时不接管」。
 * 此时显示任何标签都会暗示「这台 Hub 判断过、结论是某种状态」，属于凭空捏造。
 * 所以直接不输出任何内容，旧 Hub 的界面与升级前完全一致。
 *
 * ## 原因不能只靠 tooltip
 *
 * 手机/平板上没有 hover，只挂 title 等于用户永远看不到原因。
 * 因此布局是：状态标签 + 一行截断的原因文本（本身就可见），
 * tooltip 只是让桌面用户能看全文的补充。
 */
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import type { AutoAdoptStatus } from '@/types/stack'

interface Props {
  status?: AutoAdoptStatus
  /** 原因文本；后端给的是人话整句，可能较长 */
  reason?: string
}

const props = withDefaults(defineProps<Props>(), { status: 'unknown', reason: '' })

/** 状态 → 中文文案与配色；unknown 为 null 表示不渲染 */
const META: Record<AutoAdoptStatus, { label: string; type: 'default' | 'info' | 'warning' | 'error' } | null> =
  {
    // 后台正在处理：中性提示，不是错误
    pending: { label: '接管中', type: 'info' },
    /*
     * skipped：Hub 判断这个项目不适合自动接管（例如用了 build、
     * 或配置里有它不理解的写法）。不是故障，用中性灰。
     */
    skipped: { label: '暂不接管', type: 'default' },
    /*
     * failed：尝试过但失败了，用户可能需要看一眼原因并手动处理。
     * 用 error 色把它和「暂时不接管」区分开——前者要关注，后者不用。
     */
    failed: { label: '接管失败', type: 'error' },
    // 前端兜底：不渲染任何东西
    unknown: null
  }

const meta = computed(() => META[props.status] ?? null)

/** 只有在 skipped / failed 时才谈原因：pending 没必要，unknown 不允许推测 */
const showReason = computed(
  () => props.reason.trim() !== '' && (props.status === 'skipped' || props.status === 'failed')
)
</script>

<template>
  <div v-if="meta !== null" class="auto-adopt">
    <n-tag size="small" :bordered="false" :type="meta.type">{{ meta.label }}</n-tag>
    <!--
      原因直接可见（一行截断），title 让桌面用户能看全文。
      不放进只有 hover 才出现的 tooltip 里：手机上没有 hover。
    -->
    <span v-if="showReason" class="auto-adopt__reason" :title="reason">{{ reason }}</span>
  </div>
</template>

<style scoped>
.auto-adopt {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
}

/*
  一行截断：原因可能是整句中文，不截断会把表格撑破、挤掉后面的列。
  截断后用 title 提供全文，用户也能在详情里看到（后续若加）。
*/
.auto-adopt__reason {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
  color: var(--arkade-text-tertiary);
}
</style>
