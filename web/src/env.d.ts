/// <reference types="vite/client" />

// Vue 单文件组件的模块声明：让 TS 能识别 *.vue 导入
declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  // 这里用 unknown 而非 any，保持严格模式；组件内部类型由 vue-tsc 从 SFC 推导
  const component: DefineComponent<Record<string, unknown>, Record<string, unknown>, unknown>
  export default component
}
