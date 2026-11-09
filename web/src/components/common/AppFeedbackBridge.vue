<script setup lang="ts">
/**
 * 反馈桥（无渲染组件）
 *
 * 背景：Naive UI 的 useMessage()/useDialog()/useNotification() 只能在有对应
 * Provider 的 setup 上下文里调用；而 store、路由守卫、异步业务流程里也需要弹提示。
 *
 * 做法：在本组件（App.vue 中位于所有 Provider 之内）实例化这些 API，
 * 写入 utils/feedback 的模块级持有者，任何位置都能通过 getMessage()/getDialog() 取用。
 * 组件本身不渲染任何结构，只向下透传 <slot />。
 */
import { useDialog, useMessage, useNotification } from 'naive-ui'
import { setFeedbackApis } from '@/utils/feedback'

const message = useMessage()
const dialog = useDialog()
const notification = useNotification()

setFeedbackApis({ message, dialog, notification })

defineOptions({ name: 'AppFeedbackBridge' })
</script>

<template>
  <slot />
</template>
