import { fileURLToPath, URL } from 'node:url'
import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

/**
 * Vite 配置
 *
 * 关键点：
 * 1. 产物目录固定为 web/dist —— Hub（Go）会把它作为静态文件目录托管，
 *    前端打包后直接由后端起服务，不额外引入 nginx。
 * 2. 开发期 /api 代理到 http://127.0.0.1:8080（Hub 的监听地址），
 *    这样前端代码里所有请求都写同源相对路径 /api/xxx，开发与生产一致，
 *    无需任何环境变量或基地址切换。
 * 3. 认证依赖 HttpOnly Cookie，因此代理要透传 Cookie（默认即支持，仅在此说明）。
 */
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url))
    }
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true
      }
    }
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    // Hub 直接托管 dist，开启 sourcemap 便于线上排障（体积可接受）
    sourcemap: false,
    chunkSizeWarningLimit: 1200
  }
})
