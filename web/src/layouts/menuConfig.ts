/**
 * 侧边菜单配置
 *
 * 抽成独立模块的原因：同一份菜单要同时喂给两处——
 * 桌面/平板用的 `NLayoutSider` 内嵌 NMenu，手机用的 `NDrawer` 内嵌 NMenu。
 * 若各写一份，将来加菜单项必然漏改一处。
 *
 * ## 结构：静态入口 + 动态操作菜单
 *
 * 1. **服务器入口**（`/agents`）永远在顶部，随时可回到列表切换服务器；
 * 2. **服务器操作菜单**（容器/编排/镜像/网络/卷）只在「已选中某台服务器」时出现，
 *    也就是 URL 命中 `/agents/:id/*` 时才展开。未选中时只显示服务器入口。
 *
 * 这一结构直接对应客户原话：
 * 「选择好服务器之后，左侧菜单显示当前服务器的操作类表，
 *   比如容器，容器编排，本地镜像，网络，储存卷等」。
 */

import { h } from 'vue'
import { NIcon, type MenuOption } from 'naive-ui'
import AppIcon from '@/components/common/AppIcon.vue'
import type { IconName } from '@/components/common/iconNames'

/** 菜单项定义：比 MenuOption 更窄，只保留本项目用到的字段 */
export interface MenuItemDef {
  /** 路由名，与 router/index.ts 中的 name 一致，用于编程式跳转 */
  routeName: string
  /** 菜单文案 */
  label: string
  /** 图标名（内联 SVG） */
  icon: IconName
}

/**
 * 服务器操作菜单项。
 *
 * 注意：这些路由都带 `:id` 参数，跳转时必须把当前服务器 id 一起传进去，
 * 因此这里只存「路由名 + 文案 + 图标」，路径拼接交给 buildMenuOptions。
 */
export const AGENT_OPERATION_ITEMS: MenuItemDef[] = [
  { routeName: 'agent-containers', label: '容器', icon: 'container' },
  { routeName: 'agent-stacks', label: '容器编排', icon: 'stack' },
  { routeName: 'agent-images', label: '本地镜像', icon: 'image' },
  { routeName: 'agent-networks', label: '网络', icon: 'network' },
  { routeName: 'agent-volumes', label: '存储卷', icon: 'volume' }
]

/** 顶部固定入口：服务器列表 */
const SERVER_ENTRY: MenuItemDef = { routeName: 'agents', label: '服务器', icon: 'server' }

/** 菜单分组：分组标题为空字符串时不渲染标题行 */
export interface MenuGroupDef {
  key: string
  title: string
  items: MenuItemDef[]
}

/** 把图标包成 NMenu 需要的 VNode 渲染器 */
function renderIcon(icon: IconName) {
  return (): ReturnType<typeof h> =>
    h(NIcon, { size: 18 }, { default: () => h(AppIcon, { name: icon }) })
}

/**
 * 单项 → NMenu option。
 *
 * 注意：这里**不能**把 agentId 塞进 option 的任何展示性字段。
 * 起初用了 `extra: params.id`，结果 NMenu 会把它当作右侧附加内容渲染出来，
 * 菜单上直接显示出「容器 ag-1」这种字样。
 * 服务器 id 由 SiteMenu 自己从 props 取（它已经知道 agentId），
 * 菜单配置只负责文案与图标，不携带跳转态数据。
 */
function toMenuOption(item: MenuItemDef): MenuOption {
  return {
    // key 用路由名：与路由表天然一一对应，避免手写路径字符串
    key: item.routeName,
    label: item.label,
    icon: renderIcon(item.icon)
  }
}

/**
 * 生成 NMenu 的 options。
 *
 * @param agentId 当前选中的服务器 id；为 null 时**不展开**操作菜单
 *                （未选中服务器时只显示「服务器」入口）
 *
 * 关于分组标题：
 * - 只有「服务器」一项时不给标题，避免纯占一行高度；
 * - 展开操作菜单后有两组，此时给操作组加标题「当前服务器」，
 *   让用户清楚下面这些是针对某台服务器的操作，而不是全局入口。
 */
export function buildMenuOptions(agentId: string | null): MenuOption[] {
  const groups: MenuOption[] = [
    {
      type: 'group',
      key: 'entry',
      label: '',
      children: [toMenuOption(SERVER_ENTRY)]
    } as MenuOption
  ]

  if (agentId !== null) {
    groups.push({
      type: 'group',
      key: 'agent-operations',
      label: '当前服务器',
      children: AGENT_OPERATION_ITEMS.map((item) => toMenuOption(item))
    } as MenuOption)
  }

  return groups
}

/**
 * 由当前路由推导出应高亮的菜单 key。
 *
 * 直接使用路由名作为菜单 key——改造后操作菜单的路由名与菜单 key 完全一致
 * （agent-containers / agent-stacks / ...），不再需要「子路由映射回父菜单」的映射表。
 *
 * 两个例外：
 * - 服务器列表页路由名是 `agents`，而菜单 key 也是 `agents`，天然对上；
 * - 编排详情页路由名是 `agent-stack-detail`，需要映射回 `agent-stacks`，
 *   否则进入详情后左侧「容器编排」不高亮，用户会迷失位置。
 */
const ROUTE_TO_MENU_KEY: Record<string, string> = {
  agents: 'agents',
  'agent-containers': 'agent-containers',
  'agent-stacks': 'agent-stacks',
  // 编排详情/新建属于「容器编排」这一菜单项的下级页面
  'agent-stack-detail': 'agent-stacks',
  'agent-stack-create': 'agent-stacks',
  'agent-images': 'agent-images',
  'agent-networks': 'agent-networks',
  'agent-volumes': 'agent-volumes'
}

/** 根据路由名得到高亮的菜单 key；未知路由返回 null（不高亮任何项） */
export function resolveActiveMenuKey(routeName: string | symbol | null | undefined): string | null {
  if (typeof routeName !== 'string') return null
  return ROUTE_TO_MENU_KEY[routeName] ?? null
}

/** 所有可点击菜单项的 routeName 集合：SiteMenu 用它判断 key 是否可直接跳转 */
export function isMenuRouteName(key: string): boolean {
  return key === SERVER_ENTRY.routeName || AGENT_OPERATION_ITEMS.some((item) => item.routeName === key)
}
