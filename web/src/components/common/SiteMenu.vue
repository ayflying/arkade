<script setup lang="ts">
/**
 * 侧边菜单内容（NMenu 封装）
 *
 * 为什么封装：桌面/平板的 NLayoutSider 与手机的 NDrawer 里装的是**同一份**菜单，
 * 高亮、跳转、折叠态提示等行为必须完全一致。抽成一个组件后，
 * 布局组件只负责「把菜单放进哪个容器」，不再重复菜单逻辑。
 *
 * 高亮规则：activeKey 由父组件传入（已通过 menuConfig 做过映射），
 * 这里只做纯渲染、不自己读路由，保证高亮的数据来源唯一。
 */
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { NMenu } from 'naive-ui'
import { buildMenuOptions } from '@/layouts/menuConfig'
import { SIDER_WIDTH_COLLAPSED } from '@/composables/useResponsive'

interface Props {
  /** 当前高亮的菜单 key；null 表示不高亮 */
  activeKey: string | null
  /** 当前选中的服务器 id；为 null 时不展开「当前服务器」操作菜单 */
  agentId: string | null
  /** 是否折叠为图标窄栏（平板档）；折叠后 NMenu 只显示图标 */
  collapsed?: boolean
  /** 导航完成后的回调（手机端用它关闭抽屉） */
  onNavigate?: () => void
}

const props = withDefaults(defineProps<Props>(), {
  collapsed: false,
  onNavigate: undefined
})

const router = useRouter()

/**
 * 菜单项随「是否选中服务器」变化，因此必须用 computed。
 *
 * 不用 ref 缓存一次的原因：agentId 会随用户切换服务器实时变化，
 * 操作菜单里的跳转参数必须跟着变，否则切了服务器仍跳到旧服务器。
 */
const menuOptions = computed(() => buildMenuOptions(props.agentId))

/**
 * 菜单点击：key 即 routeName（约定见 menuConfig.buildMenuOptions）。
 *
 * 操作类菜单（容器/编排/镜像/网络/卷）的路由都带 `:id` 参数，
 * 因此跳转时必须把当前服务器 id 一并传入；「服务器」入口没有该参数。
 * 若 agentId 为空而点了操作菜单（理论上不会发生，因为此时不渲染），
 * 退回服务器列表，避免产生 /agents//containers 这类坏路径。
 *
 * 边界处理：
 * - 点击当前已激活项时不重复 push，避免多余导航与高亮闪烁；
 * - 跳转失败（如路由不存在）也要关抽屉，否则抽屉挡在错误页前面。
 */
function handleUpdateValue(key: string): void {
  const isServerEntry = key === 'agents'
  const params = isServerEntry ? {} : { id: props.agentId ?? '' }

  // 参数缺失时不要拼出坏路径，直接回服务器列表
  if (!isServerEntry && (props.agentId === null || props.agentId === '')) {
    void router.push({ name: 'agents' }).finally(() => props.onNavigate?.())
    return
  }

  if (router.currentRoute.value.name === key) {
    props.onNavigate?.()
    return
  }

  void router
    .push({ name: key, params })
    .catch(() => undefined)
    .finally(() => props.onNavigate?.())
}
</script>

<template>
  <n-menu
    :options="menuOptions"
    :value="activeKey"
    :collapsed="collapsed"
    :collapsed-width="SIDER_WIDTH_COLLAPSED"
    :collapsed-icon-size="18"
    :indent="18"
    @update:value="handleUpdateValue"
  />
</template>
