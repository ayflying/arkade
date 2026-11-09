/**
 * 全局反馈出口（message / dialog / notification）
 *
 * Naive UI 的这些 API 依赖组件上下文，不能在模块顶层直接用。
 * AppFeedbackBridge.vue 在 Provider 内把它们注册到这里，
 * 从而让 store / 路由守卫 / 工具函数也能弹提示，且全站只实例化一次。
 *
 * 边界：组件挂载前调用 getMessage() 会返回 null，调用方需判空（或提供 toast 安全封装）。
 */

import type { DialogApi, MessageApi, NotificationApi } from 'naive-ui'

interface FeedbackApis {
  message: MessageApi
  dialog: DialogApi
  notification: NotificationApi
}

let apis: FeedbackApis | null = null

/** 由桥接组件调用，注册 API 实例 */
export function setFeedbackApis(next: FeedbackApis): void {
  apis = next
}

/** 获取 message API；未注册时返回 null */
export function getMessage(): MessageApi | null {
  return apis?.message ?? null
}

/** 获取 dialog API；未注册时返回 null */
export function getDialog(): DialogApi | null {
  return apis?.dialog ?? null
}

/** 获取 notification API；未注册时返回 null */
export function getNotification(): NotificationApi | null {
  return apis?.notification ?? null
}

/**
 * 安全弹错误提示：即便桥接尚未就绪也不抛错（应用启动早期可能触发）。
 * 走 error 类型，让用户明确知道「刚才那个操作失败了」。
 */
export function toastError(text: string): void {
  const message = getMessage()
  if (message) {
    message.error(text)
  } else {
    // 兜底：桥接未就绪时至少让错误在控制台可见
    console.error('[Arkade]', text)
  }
}

/** 安全弹成功提示 */
export function toastSuccess(text: string): void {
  getMessage()?.success(text)
}

/**
 * 安全弹警告提示。
 *
 * 与 toastError 的区别：这不代表操作失败，而是「操作完成了，但有需要你注意的地方」。
 * 典型场景是编排保存：内容已存进 Hub，但 Agent 离线导致尚未下发到服务器
 * （后端用 sync_ok:false + sync_hint 表达）。用错误色会误导用户以为白写了。
 *
 * 文字可能较长（后端的 sync_hint 是一整句中文说明），因此延长显示时长。
 */
export function toastWarning(text: string): void {
  const message = getMessage()
  if (message) {
    message.warning(text, { duration: 6000 })
  } else {
    console.warn('[Arkade]', text)
  }
}
