<script setup lang="ts">
/**
 * 概览页统计卡片
 *
 * 单张卡片：一个大数字 + 标题 + 可选的补充说明（如「3 台离线」）。
 * 抽成组件而不是在页面里复制 6 遍模板，保证 6 张卡片字重、间距完全一致。
 */
import { computed } from 'vue'

interface Props {
  /** 卡片标题，如「服务器」 */
  label: string
  /** 主数值 */
  value: number
  /** 数值单位（可选），如「台」 */
  unit?: string
  /**
   * 补充说明文字，如「在线 2 / 共 3」。
   * 与 value 分离的原因：value 是「总量」，说明是「其中多少符合某条件」，
   * 两者语义不同，合并成一个字符串会让数字失去统一的对齐基线。
   */
  hint?: string
  /**
   * 色调：normal 中性、good 积极（如运行中）、warn 需要关注（如离线）。
   * 用它而不是让调用方传色值，避免出现各页面各挑一个颜色的情况。
   */
  tone?: 'normal' | 'good' | 'warn'
}

const props = withDefaults(defineProps<Props>(), {
  unit: '',
  hint: '',
  tone: 'normal'
})

const valueClass = computed(() => `stat-card__value stat-card__value--${props.tone}`)
</script>

<template>
  <div class="stat-card">
    <div class="stat-card__label">{{ label }}</div>
    <div class="stat-card__main">
      <span :class="valueClass">{{ value }}</span>
      <span v-if="unit !== ''" class="stat-card__unit">{{ unit }}</span>
    </div>
    <div v-if="hint !== ''" class="stat-card__hint">{{ hint }}</div>
  </div>
</template>

<style scoped>
.stat-card {
  background: #fff;
  border-radius: 8px;
  padding: 14px 16px;
  box-shadow: 0 1px 2px rgba(31, 34, 37, 0.04);
  /* min-width: 0 是关键：栅格子项默认 min-width:auto，
     内容（长数字/长文案）会把格子撑破，导致窄屏横向溢出 */
  min-width: 0;
}

.stat-card__label {
  font-size: 12px;
  color: #8b9099;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.stat-card__main {
  display: flex;
  align-items: baseline;
  gap: 4px;
  margin-top: 6px;
}

.stat-card__value {
  font-size: 24px;
  font-weight: 600;
  line-height: 1.2;
  /* 数字用等宽字形，多张卡片并排时数字宽度一致、不会左右跳动 */
  font-variant-numeric: tabular-nums;
}

.stat-card__value--normal {
  color: #1f2225;
}

.stat-card__value--good {
  color: #18a058;
}

/* 需要关注的数量用橙色而非红色：红色留给错误/危险操作，避免满屏告警色 */
.stat-card__value--warn {
  color: #f0a020;
}

.stat-card__unit {
  font-size: 12px;
  color: #8b9099;
}

.stat-card__hint {
  margin-top: 4px;
  font-size: 12px;
  color: #8b9099;
  /* 说明可能较长（如「127.0.0.1 上 3 个未纳管」），允许换行避免撑破卡片 */
  overflow-wrap: anywhere;
}
</style>
