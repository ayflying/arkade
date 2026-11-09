import { useAuthStore } from '@/stores/auth'
import { HOME_PATH, LOGIN_PATH, router } from './index'

/**
 * 全局路由守卫：登录态判定
 *
 * 逻辑要点：
 * 1. 页面首次加载（或刷新）时，内存里的 user 一定是 null，但 HttpOnly Cookie 可能还有效，
 *    所以先 await store.fetchMe() 向后端确认，再决定放行或跳登录页——否则刷新必被踢出。
 * 2. public 路由（登录页）：已登录用户访问时重定向到 /agents，避免重复登录。
 * 3. 业务路由：未登录 → 跳登录页，并带上 redirect 查询参数，登录成功后回到原页面。
 */
router.beforeEach(async (to) => {
  const auth = useAuthStore()

  // 尚未确认会话：向后端询问一次（内部会吞掉 401 并置 user=null）
  if (!auth.initialized) {
    await auth.fetchMe()
  }

  const isPublic = to.meta.public === true

  if (isPublic) {
    // 已登录还去登录页，直接送到业务首页（概览，不再是服务器列表）
    return auth.isLoggedIn ? { path: HOME_PATH } : true
  }

  if (!auth.isLoggedIn) {
    return {
      path: LOGIN_PATH,
      // 记录来源，登录成功后回跳；只记录站内完整路径
      query: to.fullPath !== '/' ? { redirect: to.fullPath } : undefined
    }
  }

  return true
})

/** 同步浏览器标题：登录页与业务页各自显示有意义的名字 */
router.afterEach((to) => {
  const title = typeof to.meta.title === 'string' ? to.meta.title : ''
  document.title = title !== '' ? `${title} · Arkade 容器方舟` : 'Arkade 容器方舟'
})
