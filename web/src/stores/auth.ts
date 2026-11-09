/**
 * 认证状态 Store
 *
 * 职责：保存当前登录用户，提供 login / logout / fetchMe。
 * 它是路由守卫判断「是否已登录」的唯一数据源，因此：
 * - `initialized` 标记是否已经向后端确认过会话（首次进入刷新页面时 Cookie 可能仍有效，
 *   不能因为内存里 user 为 null 就直接判未登录，否则每次刷新都被踢回登录页）；
 * - 任何接口返回 401 时调用方可通过 clearLocal 让前端回到未登录态。
 */

import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import * as authApi from '@/api/auth'
import type { LoginPayload, User } from '@/types/auth'

export const useAuthStore = defineStore('auth', () => {
  /** 当前登录用户；null 表示未登录 */
  const user = ref<User | null>(null)
  /** 是否已向后端确认过会话状态（仅在 fetchMe 成功或失败后被置 true） */
  const initialized = ref(false)
  /** 登录请求进行中：登录按钮 loading 用 */
  const loggingIn = ref(false)

  /** 是否已登录（供路由守卫与界面 v-if 使用） */
  const isLoggedIn = computed(() => user.value !== null)

  /** 展示用名字：优先 display_name，没有则退回 username */
  const displayName = computed<string>(() => user.value?.display_name || user.value?.username || '')

  /**
   * 登录。
   * 失败直接抛给页面（页面负责用 n-message 展示中文文案），store 不吞错。
   */
  async function login(payload: LoginPayload): Promise<void> {
    loggingIn.value = true
    try {
      user.value = await authApi.login(payload)
      initialized.value = true
    } finally {
      loggingIn.value = false
    }
  }

  /**
   * 退出登录。
   *
   * 边界：即使后端 logout 报错（比如会话早已过期），前端也必须清掉本地用户态，
   * 否则用户会卡在一个「看似登录但所有请求都 401」的界面里。
   */
  async function logout(): Promise<void> {
    try {
      await authApi.logout()
    } finally {
      clearLocal()
    }
  }

  /**
   * 向后端确认会话。
   *
   * 返回 true 表示已登录。注意：网络错误（后端没起来）也视为未登录并结束初始化，
   * 由登录页给出「无法连接服务器」的提示，避免路由守卫无限等待。
   */
  async function fetchMe(): Promise<boolean> {
    try {
      user.value = await authApi.fetchMe()
      return true
    } catch {
      user.value = null
      return false
    } finally {
      initialized.value = true
    }
  }

  /** 仅清本地状态，不发请求（供 401 拦截、登出兜底使用） */
  function clearLocal(): void {
    user.value = null
    initialized.value = true
  }

  return { user, initialized, loggingIn, isLoggedIn, displayName, login, logout, fetchMe, clearLocal }
})
