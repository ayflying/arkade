<script setup lang="ts">
/**
 * 根组件
 *
 * 只做两件事：
 * 1. 用 NConfigProvider 注入主题与全局中文 locale（Naive UI 自带的空态、分页等文案要中文）；
 * 2. 按 message → dialog → notification 的顺序嵌套 Provider，并用 AppFeedbackBridge
 *    在 Provider 内部把反馈 API 注册到全局出口，使 store / 守卫 / 工具函数也能弹提示。
 *    注意 order 有讲究：桥接组件必须位于所有 Provider 之内，才能拿到完整上下文。
 */
import {
  NConfigProvider,
  NDialogProvider,
  NMessageProvider,
  NNotificationProvider,
  dateZhCN,
  zhCN
} from 'naive-ui'
import AppFeedbackBridge from '@/components/common/AppFeedbackBridge.vue'
import { themeOverrides } from '@/styles/theme'
</script>

<template>
  <n-config-provider :theme-overrides="themeOverrides" :locale="zhCN" :date-locale="dateZhCN">
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
