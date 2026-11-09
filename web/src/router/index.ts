import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

/**
 * 路由表
 *
 * - /login        登录页（不需要认证）
 * - /agents       服务器列表（业务页，需登录）
 * - /agents/:id   某台服务器的容器列表（业务页，需登录）
 *
 * 用 history 模式：Hub 托管静态文件时必须把未命中的路径回落到 index.html
 * （这一点需要 Hub 侧配合，属于后端职责；前端无法凭空解决）。
 */
const routes: RouteRecordRaw[] = [
  {
    path: '/',
    // 根路径不单独做页面，直接重定向到服务器列表
    redirect: '/agents'
  },
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { public: true, title: '登录' }
  },
  {
    path: '/agents',
    component: () => import('@/layouts/AppLayout.vue'),
    children: [
      {
        path: '',
        name: 'agents',
        component: () => import('@/views/AgentListView.vue'),
        meta: { title: '服务器' }
      },
      {
        path: ':id',
        name: 'agent-containers',
        component: () => import('@/views/ContainerListView.vue'),
        props: true,
        meta: { title: '容器' }
      }
    ]
  },
  {
    // 兜底：未知路径回到服务器列表，避免白屏
    path: '/:pathMatch(.*)*',
    redirect: '/agents'
  }
]

export const router = createRouter({
  history: createWebHistory(),
  routes
})

/** 登录页路径常量：守卫与退出登录后跳转共用，避免字符串写错 */
export const LOGIN_PATH = '/login'
