<script setup lang="ts">
/**
 * 内联 SVG 图标组件
 *
 * 为什么自建而不是引入 @vicons/ionicons5：
 * 1. 项目基调是「简洁优先、不轻易加依赖」，而 Naive UI 自带的 NIcon 只提供图标外壳
 *    （尺寸/颜色/深度），不提供具体图形；
 * 2. 首期只需要个位数图标（服务器 / 汉堡 / 折叠 / 用户 / 退出 / 箭头），
 *    为几个图形多装一个图标库不划算。
 *
 * 关键实现约定：
 * - 统一 `stroke="currentColor"` + `fill="none"`（线条风格），
 *   这样放进 NMenu 后会跟随其选中态的 color 自动变色，无需在 JS 里传颜色；
 * - viewBox 统一 24x24，实际尺寸交给外层 NIcon 的 size 属性控制；
 * - stroke-width 默认 1.6：在常用 18px 渲染尺寸下最接近系统图标的视觉粗细。
 */
import type { IconName } from './iconNames'

interface Props {
  /** 图标名，类型见 iconNames.ts；写错会 TS 报错 */
  name: IconName
  /** 线宽，默认 1.6 */
  strokeWidth?: number
}

withDefaults(defineProps<Props>(), { strokeWidth: 1.6 })

/**
 * 图标名 → SVG path 列表。
 *
 * 用「字符串路径数组」而不是每个图标一个 .vue 文件：这些图标都是纯线条、无交互，
 * 集中在同一个文件里可读性更好，也避免为 6 个图标创建 6 个碎片文件。
 */
const ICON_PATHS: Record<IconName, string[]> = {
  // 服务器：两层机架 + 指示灯，语义与「服务器列表」直接对应
  server: ['M4 5h16v5H4z', 'M4 14h16v5H4z', 'M7.5 7.5h.01', 'M7.5 16.5h.01'],
  // 容器：立方体（Docker 风格的三维箱体）
  container: ['M12 3l8 4.5v9L12 21l-8-4.5v-9z', 'M12 12l8-4.5', 'M12 12v9', 'M12 12L4 7.5'],
  // 容器编排：三层堆叠，表达「多个服务的组合」
  stack: ['M12 3.5l8.5 4-8.5 4-8.5-4z', 'M3.5 12l8.5 4 8.5-4', 'M3.5 16.5l8.5 4 8.5-4'],
  // 本地镜像：两个叠放的方块（镜像分层）
  image: ['M4 9.5h10v10H4z', 'M8 9.5v-4h10v10h-4', 'M6.5 16.5l2-2 1.5 1.5 2-2.5'],
  // 网络：三个节点互连
  network: ['M12 4.5a2 2 0 100 4 2 2 0 000-4z', 'M5 15.5a2 2 0 100 4 2 2 0 000-4z', 'M19 15.5a2 2 0 100 4 2 2 0 000-4z', 'M12 8.5v3.5M12 12l-5.5 3.5M12 12l5.5 3.5'],
  // 存储卷：圆柱体
  volume: ['M12 4c4.4 0 8 1.3 8 3s-3.6 3-8 3-8-1.3-8-3 3.6-3 8-3z', 'M4 7v10c0 1.7 3.6 3 8 3s8-1.3 8-3V7', 'M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3'],
  // 汉堡菜单：三条横线
  menu: ['M4 7h16', 'M4 12h16', 'M4 17h16'],
  // 折叠：左右双箭头，表示侧边栏收/展
  collapse: ['M9 6l-4 6 4 6', 'M15 6l4 6-4 6'],
  // 用户：头 + 肩
  user: ['M12 12a4 4 0 100-8 4 4 0 000 8z', 'M4.5 20a7.5 7.5 0 0115 0'],
  // 退出登录：门 + 向外箭头
  logout: ['M9 21H5a1 1 0 01-1-1V4a1 1 0 011-1h4', 'M16 17l5-5-5-5', 'M21 12H9'],
  // 右箭头
  'chevron-right': ['M9 6l6 6-6 6'],
  // 左箭头
  'chevron-left': ['M15 6l-6 6 6 6'],
  // 刷新：环形箭头
  refresh: ['M20 12a8 8 0 11-2.4-5.7', 'M20 4v5h-5'],
  // 删除：垃圾桶
  trash: ['M4 7h16', 'M9 7V5a1 1 0 011-1h4a1 1 0 011 1v2', 'M6 7l1 12a1 1 0 001 1h8a1 1 0 001-1l1-12', 'M10 11v6', 'M14 11v6'],
  // 警告：三角感叹号
  warning: ['M12 4.5L2.8 20h18.4z', 'M12 10v4', 'M12 17h.01'],
  // 概览：仪表盘（四个区块，表达「总体视图」）
  overview: ['M4 4h7v6H4z', 'M13 4h7v4h-7z', 'M13 10h7v10h-7z', 'M4 12h7v8H4z'],
  // 审计日志：带勾选的清单
  audit: ['M6 3h9l4 4v14a1 1 0 01-1 1H6a1 1 0 01-1-1V4a1 1 0 011-1z', 'M8.5 12l2 2 3.5-3.5', 'M8.5 17h7'],
  // 新增：加号
  plus: ['M12 5v14', 'M5 12h14'],
  // 编辑：铅笔
  edit: ['M4 20h4L20 8l-4-4L4 16z', 'M14 6l4 4'],
  // 禁用：圆形加斜杠
  ban: ['M12 21a9 9 0 100-18 9 9 0 000 18z', 'M6 6l12 12'],
  // 启用/成功：对勾
  check: ['M5 13l4.5 4.5L19 7']
}
</script>

<template>
  <svg
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    :stroke-width="strokeWidth"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    focusable="false"
  >
    <path v-for="(d, index) in ICON_PATHS[name]" :key="index" :d="d" />
  </svg>
</template>

<style scoped>
/* 撑满外层 NIcon 的尺寸；display:block 消除行内元素的基线间隙 */
svg {
  display: block;
  width: 100%;
  height: 100%;
}
</style>
