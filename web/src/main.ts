/**
 * Arkade 前端应用入口
 *
 * 依赖装配顺序有讲究：
 * 1. 先创建 Pinia（路由守卫里会用到 store）；
 * 2. 再注册路由守卫并挂载路由；
 * 3. 最后 mount，保证首个页面的守卫能拿到已初始化的 store。
 */

import { createPinia } from 'pinia'
import { createApp } from 'vue'
import App from './App.vue'
// 注册路由守卫（import 即生效，必须在挂载前执行）
import './router/guards'
import { router } from './router'
import './styles/global.css'

const app = createApp(App)

app.use(createPinia())
app.use(router)

app.mount('#app')
