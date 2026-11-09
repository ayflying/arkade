/**
 * 未保存改动的离开保护
 *
 * 覆盖两类离开，两者机制完全不同，不能混为一谈：
 *
 * 1. **路由内导航**（点侧边栏、浏览器前进/后退、程序化 push）：
 *    走 `onBeforeRouteLeave` / `onBeforeRouteUpdate`，可自定义确认框。
 *
 * 2. **整页卸载**（硬刷新 F5、关标签页、手输地址回车）：
 *    Vue Router **完全不参与**，路由守卫拦不住。唯一可用的是
 *    `beforeunload`，且浏览器只允许弹它自己的通用确认框，
 *    文案不可定制，现代浏览器还要求页面已有过用户交互。
 *
 * 因此 `beforeunload` 只是最后一道兜底，不能宣称它「拦住了硬刷新」。
 */

import { onMounted, onUnmounted } from 'vue'
import { onBeforeRouteLeave, onBeforeRouteUpdate } from 'vue-router'
import { confirmAction } from '@/utils/confirm'

export interface UseUnsavedGuardOptions {
  /** 当前是否有未保存改动 */
  isDirty: () => boolean
  /** 确认离开后、真正跳转前的清理（关面板等） */
  onLeaveConfirmed?: () => void
}

export interface UseUnsavedGuardReturn {
  /** 供页面在「自己发起的动作」（如打开另一文件）前调用 */
  confirmLeaveDirty: () => Promise<boolean>
}

/**
 * 安装未保存保护。
 *
 * 必须在 setup 期间调用：内部用到的路由守卫是组合式 API，
 * 只在该阶段有效。
 */
export function useUnsavedGuard(options: UseUnsavedGuardOptions): UseUnsavedGuardReturn {
  const { isDirty, onLeaveConfirmed } = options

  /** 有未保存改动时确认离开；true = 可以继续 */
  async function confirmLeaveDirty(): Promise<boolean> {
    if (!isDirty()) return true
    return await confirmAction({
      title: '放弃未保存的修改？',
      content: '这个文件有未保存的修改。离开后修改会丢失，且无法找回。\n\n确定要离开吗？',
      positiveText: '放弃修改',
      negativeText: '继续编辑',
      danger: true
    })
  }

  // 同路由换参数（切目录、切卷）时组件不重建，走 update 守卫
  onBeforeRouteUpdate(async () => {
    if (!(await confirmLeaveDirty())) return false
    onLeaveConfirmed?.()
    return true
  })

  // 离开本页（后退、侧边栏、程序化跳转）走 leave 守卫
  onBeforeRouteLeave(async () => await confirmLeaveDirty())

  /**
   * 整页卸载兜底。
   *
   * preventDefault 与 returnValue 都要设置：现代浏览器看前者，
   * 老浏览器只认后者。浏览器不会显示我们写的文案，也无法得知用户选了哪个。
   */
  function handleBeforeUnload(event: BeforeUnloadEvent): void {
    if (!isDirty()) return
    event.preventDefault()
    event.returnValue = ''
  }

  onMounted(() => {
    window.addEventListener('beforeunload', handleBeforeUnload)
  })

  onUnmounted(() => {
    // 路由复用时组件会被销毁重建，不移除会累积重复监听
    window.removeEventListener('beforeunload', handleBeforeUnload)
  })

  return { confirmLeaveDirty }
}
