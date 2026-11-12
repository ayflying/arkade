import type { RouteRecordRaw } from 'vue-router';
export const administrationRoutes: RouteRecordRaw[] = [
    { path: '/register', name: 'register', component: () => import('@/views/RegisterView.vue'), meta: { public: true, title: '注册账户' } },
    { path: '/', component: () => import('@/layouts/AppLayout.vue'), redirect: '/overview', children: [
            { path: 'account', name: 'account', component: () => import('@/views/AccountView.vue'), meta: { title: '个人设置' } },
            { path: 'users', name: 'users', component: () => import('@/views/UsersView.vue'), meta: { title: '用户管理', adminOnly: true } },
            { path: 'groups', name: 'groups', component: () => import('@/views/GroupsView.vue'), meta: { title: '用户组与授权', adminOnly: true } },
            { path: 'settings', name: 'settings', component: () => import('@/views/SettingsView.vue'), meta: { title: '系统设置', adminOnly: true } },
            { path: 'forbidden', name: 'forbidden', component: () => import('@/views/ForbiddenView.vue'), meta: { title: '无权访问' } }
        ] }
];
