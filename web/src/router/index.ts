import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'

/**
 * 路由表
 *
 * 结构（改造后）：
 * - /login                        登录页（不需要认证）
 * - /agents                       服务器列表
 * - /agents/:id/containers        容器的容器列表    ← 旧路径 /agents/:id 重定向到此
 * - /agents/:id/stacks            容器编排列表
 * - /agents/:id/stacks/new        新建编排
 * - /agents/:id/stacks/:name      编排详情
 * - /agents/:id/images            本地镜像
 * - /agents/:id/networks          网络
 * - /agents/:id/volumes           存储卷
 *
 * 为什么把 `:id` 提到父级路由：
 * 选中服务器后的 5 个操作页面共享同一个 `:id` 参数。若在每个子路由里各写一遍
 * `:id`，一是重复，二是切换子页面时 `route.params.id` 的引用会变化、
 * 触发不必要的组件重建。用父级 `:id` + 空路径父组件，
 * 可以让「当前服务器」在切换操作页时保持稳定。
 *
 * 用 history 模式：Hub 托管静态文件时必须把未命中的路径回落到 index.html
 * （这一点需要 Hub 侧配合，属于后端职责；前端无法凭空解决）。
 */

/** 服务器操作子路由：都挂在 /agents/:id 之下 */
const agentChildRoutes: RouteRecordRaw[] = [
  {
    path: 'containers',
    name: 'agent-containers',
    component: () => import('@/views/ContainerListView.vue'),
    props: true,
    meta: { title: '容器' }
  },
  {
    path: 'stacks',
    name: 'agent-stacks',
    component: () => import('@/views/StackListView.vue'),
    props: true,
    meta: { title: '容器编排' }
  },
  {
    path: 'stacks/new',
    name: 'agent-stack-create',
    component: () => import('@/views/StackCreateView.vue'),
    props: true,
    meta: { title: '新建编排' }
  },
  {
    // 注意：放在 stacks/new 之后，避免 'new' 被当成编排名匹配
    path: 'stacks/:name',
    name: 'agent-stack-detail',
    component: () => import('@/views/StackDetailView.vue'),
    props: true,
    meta: { title: '编排详情' }
  },
  {
    path: 'images',
    name: 'agent-images',
    component: () => import('@/views/ImageListView.vue'),
    props: true,
    meta: { title: '本地镜像' }
  },
  {
    path: 'networks',
    name: 'agent-networks',
    component: () => import('@/views/NetworkListView.vue'),
    props: true,
    meta: { title: '网络' }
  },
  {
    path: 'volumes',
    name: 'agent-volumes',
    component: () => import('@/views/VolumeListView.vue'),
    props: true,
    meta: { title: '存储卷' }
  }
]

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
        // 空路径父级：只为承载 :id 参数与子路由，自身不渲染内容
        path: ':id',
        children: [
          {
            // 旧链接兼容：/agents/xxx 曾经直接是容器页，重定向到 /containers
            path: '',
            redirect: (to) => ({ name: 'agent-containers', params: { id: to.params.id } })
          },
          ...agentChildRoutes
        ]
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
