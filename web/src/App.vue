<script setup lang="ts">
/**
 * 根组件
 *
 * 只做两件事：
 * 1. 用 NConfigProvider 注入主题与全局中文 locale（Naive UI 自带的空态、分页等文案要中文）；
 * 2. 按 message → dialog → notification 的顺序嵌套 Provider，并用 AppFeedbackBridge
 *    在 Provider 内部把反馈 API 注册到全局出口，使 store / 守卫 / 工具函数也能弹提示。
 *    注意 order 有讲究：桥接组件必须位于所有 Provider 之内，才能拿到完整上下文。
 *
 * ## 暗黑模式
 * Naive 的 darkTheme 是整棵组件树的主题来源，必须挂在这里；
 * 页面里那些自制卡片则靠 CSS 变量（styles/global.css）跟着切换。
 * 两套色值同源，改色时 styles/theme.ts 与 global.css 一起改。
 */
import {
  NConfigProvider,
  NDialogProvider,
  NMessageProvider,
  NNotificationProvider,
  darkTheme,
  dateZhCN,
  zhCN
} from 'naive-ui'
import { computed } from 'vue'
import AppFeedbackBridge from '@/components/common/AppFeedbackBridge.vue'
import { useThemeMode } from '@/composables/useThemeMode'
import { darkThemeOverrides, themeOverrides } from '@/styles/theme'

const { isDark } = useThemeMode()

/** 深色时同时换主题对象与覆盖值，否则只有背景变深、主色仍是暗的 */
const activeTheme = computed(() => (isDark.value ? darkTheme : null))
const activeOverrides = computed(() => (isDark.value ? darkThemeOverrides : themeOverrides))
</script>

<template>
  <n-config-provider
    :theme="activeTheme"
    :theme-overrides="activeOverrides"
    :locale="zhCN"
    :date-locale="dateZhCN"
  >
    <n-message-provider>
      <n-dialog-provider>
        <n-notification-provider>
          <app-feedback-bridge>
            <router-view />
          </app-feedback-bridge>
        </n-notification-provider>
      </n-dialog-provider>
    </n-message-provider>
  </n-config-provider>
</template>
