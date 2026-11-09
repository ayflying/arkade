<script setup lang="ts">
/**
 * 标签列表展示（Agent labels 是 object：label 名 → 值）
 *
 * 边界：
 * - labels 为空对象时显示「—」，不留空白；
 * - 标签数量多时最多渲染 max 个，其余折叠成「+N」，避免表格行被撑高。
 */
import { computed } from 'vue'
import { NTag, NTooltip } from 'naive-ui'

interface Props {
  labels: Record<string, string>
  /** 最多展示几个标签，超出折叠 */
  max?: number
}

const props = defineProps<Props>()

const entries = computed<Array<[string, string]>>(() => Object.entries(props.labels ?? {}))

const visible = computed(() => entries.value.slice(0, props.max ?? 4))
const hiddenCount = computed(() => Math.max(0, entries.value.length - visible.value.length))

/** 折叠部分的完整文本，用于 tooltip */
const hiddenText = computed(() =>
  entries.value
    .slice(props.max ?? 4)
    .map(([k, v]) => `${k}=${v}`)
    .join('，')
)
</script>

<template>
  <div v-if="entries.length > 0" class="label-list">
    <n-tag
      v-for="[key, value] in visible"
      :key="key"
      size="small"
      :bordered="false"
      type="default"
      class="label-list__item"
    >
      <span class="label-list__key">{{ key }}</span>
      <span class="label-list__value">{{ value }}</span>
    </n-tag>
    <n-tooltip v-if="hiddenCount > 0" trigger="hover">
      <template #trigger>
        <n-tag size="small" :bordered="false" class="label-list__item label-list__more">
          +{{ hiddenCount }}
        </n-tag>
      </template>
      {{ hiddenText }}
    </n-tooltip>
  </div>
  <span v-else class="label-list__empty">—</span>
</template>

<style scoped>
.label-list {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
}

.label-list__item {
  max-width: 180px;
}

.label-list__key {
  color: #8b9099;
  margin-right: 4px;
}

.label-list__value {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.label-list__more {
  cursor: default;
}

.label-list__empty {
  color: #b5b9c0;
}
</style>
