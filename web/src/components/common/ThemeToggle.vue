<script setup lang="ts">
/**
 * 主题切换（浅色 / 深色 / 跟随系统）
 *
 * ## 为什么是三段式下拉而不是一个"开关"
 * 只有明暗两态的开关无法表达"跟随系统"——用户点了两次就再也回不到自动跟随。
 * 而"跟随系统"恰恰是最该保留的默认值（手机夜间自动切深色）。
 *
 */
import { NButton, NDropdown, NIcon } from 'naive-ui'
import { computed, h } from 'vue'
import type { DropdownOption } from 'naive-ui'
import AppIcon from '@/components/common/AppIcon.vue'
import { useThemeMode, type ThemeMode } from '@/composables/useThemeMode'

const { mode, isDark, setMode } = useThemeMode()

/** 三种模式的文案 */
const MODE_LABEL: Record<ThemeMode, string> = {
  light: '浅色',
  dark: '深色',
  auto: '跟随系统'
}

const label = computed(() => MODE_LABEL[mode.value])

/** 图标随实际生效的明暗变化，而不是随模式名：auto + 系统深色时也显示月亮 */
const iconName = computed(() => (isDark.value ? 'moon' : 'sun'))

const options = computed<DropdownOption[]>(() =>
  (['light', 'dark', 'auto'] as ThemeMode[]).map((value) => ({
    key: value,
    label: MODE_LABEL[value],
    // 当前项打个勾，不必再单独渲染选中态
    icon: value === mode.value ? () => h(NIcon, { size: 14 }, () => h('span', {}, '✓')) : undefined
  }))
)

function onSelect(key: string): void {
  if (key === 'light' || key === 'dark' || key === 'auto') setMode(key)
}
</script>

<template>
  <n-dropdown trigger="click" :options="options" @select="onSelect">
    <n-button quaternary circle :title="`主题：${label}`" :aria-label="`主题：${label}`">
      <template #icon>
        <n-icon :size="18"><app-icon :name="iconName" /></n-icon>
      </template>
    </n-button>
  </n-dropdown>
</template>
