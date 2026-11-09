/**
 * 内置图标名定义
 *
 * 单独成文件的原因：`<script setup>` 不能导出类型（导出会被编译器拒绝），
 * 而菜单配置、布局组件都需要引用 `IconName` 类型，因此放到独立模块。
 * 图标图形本身在 AppIcon.vue 里，两者按名称一一对应。
 */

/** 支持的图标名：拼错会直接 TS 报错，避免出现「图标不渲染」的静默失败 */
export type IconName =
  | 'server' // 服务器
  | 'container' // 容器
  | 'stack' // 容器编排（多层堆叠）
  | 'image' // 本地镜像（分层方块）
  | 'network' // 网络（节点连线）
  | 'volume' // 存储卷（圆柱体）
  | 'menu' // 汉堡菜单（唤出抽屉）
  | 'collapse' // 折叠/展开侧边栏
  | 'user' // 用户
  | 'logout' // 退出登录
  | 'chevron-right' // 右箭头（面包屑/跳转暗示）
  | 'chevron-left' // 左箭头（返回）
  | 'refresh' // 刷新
  | 'trash' // 删除
  | 'warning' // 警告（drift 告警）
  | 'overview' // 概览（仪表盘）
  | 'audit' // 审计日志（带勾选的清单）
  | 'plus' // 新增/添加说明入口
  | 'edit' // 编辑
  | 'ban' // 禁用
  | 'check' // 启用/成功

/** 全部图标名的运行时列表，便于测试或调试时校验完整性 */
export const ICON_NAMES: readonly IconName[] = [
  'server',
  'container',
  'stack',
  'image',
  'network',
  'volume',
  'menu',
  'collapse',
  'user',
  'logout',
  'chevron-right',
  'chevron-left',
  'refresh',
  'trash',
  'warning',
  'overview',
  'audit',
  'plus',
  'edit',
  'ban',
  'check'
]
