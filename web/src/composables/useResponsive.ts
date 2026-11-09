/**
 * 响应式断点 composable
 *
 * 为什么用 window.matchMedia 而不是监听 resize + 比较 innerWidth：
 * 1. matchMedia 的 change 事件只在「跨越断点时」触发一次；而 resize 在拖拽窗口时每秒可能触发上百次，
 *    用 resize 比较宽度会让依赖它的 computed 频繁重算，进而引起整棵布局树重渲染；
 * 2. matchMedia 与 CSS 媒体查询共用同一套判定逻辑，JS 与 CSS 的断点不会出现分歧，
 *    也就不需要维护两处魔法数字。
 */

import { computed, onScopeDispose, readonly, ref, type ComputedRef, type Ref } from 'vue'

/**
 * 断点取值：只设两档，共三个区间。
 *
 * - **768px**：平板竖屏的典型宽度（iPad Mini 竖屏 768、多数安卓平板竖屏 800），
 *   也是 Bootstrap / Tailwind 等主流框架 `md` 档的共识值。低于它按手机处理，
 *   此时顶栏 + 侧边栏 + 表格已没有并排容纳的余地。
 * - **1024px**：iPad 横屏（1024）与小尺寸笔记本的下界，主流 `lg` 档。
 *   高于它才有足够横向空间放下 220px 侧边栏 + 内容区而不挤压表格。
 *
 * 三个区间：
 * - 手机  `< 768px`   ：侧边栏不渲染，改用抽屉
 * - 平板  `768–1023px`：侧边栏渲染但折叠为图标窄栏
 * - 桌面  `>= 1024px` ：侧边栏完整展开
 */
export const BREAKPOINT_TABLET = 768
export const BREAKPOINT_DESKTOP = 1024

/** 侧边栏展开宽度（px）：NLayoutSider 与抽屉共用，保证视觉一致 */
export const SIDER_WIDTH_EXPANDED = 220
/** 侧边栏折叠宽度（px）：需容纳一个 18px 图标 + 两侧留白，64 是主流后台的紧凑取值 */
export const SIDER_WIDTH_COLLAPSED = 64

/** 布局档位：手机 / 平板 / 桌面 */
export type LayoutTier = 'mobile' | 'tablet' | 'desktop'

/** 判断是否在浏览器环境：SSR / 测试（无 window）时不触碰 matchMedia */
function hasWindow(): boolean {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function'
}

/**
 * 创建一个跟随媒体查询的响应式布尔值。
 *
 * 边界：
 * - 非浏览器环境永久为 false，调用方按「宽屏」假定，避免 SSR 时误判成手机；
 * - 组件卸载时自动移除监听（onScopeDispose），防止热更新反复调用时监听器堆积。
 */
function useMediaQuery(query: string): Readonly<Ref<boolean>> {
  const matches = ref(false)
  if (!hasWindow()) return readonly(matches)

  const mediaQueryList = window.matchMedia(query)
  matches.value = mediaQueryList.matches

  const onChange = (event: MediaQueryListEvent): void => {
    matches.value = event.matches
  }

  // 兼容旧 Safari（<14）的 addListener；两者都存在时优先用现代 API
  if (typeof mediaQueryList.addEventListener === 'function') {
    mediaQueryList.addEventListener('change', onChange)
    onScopeDispose(() => mediaQueryList.removeEventListener('change', onChange))
  } else {
    mediaQueryList.addListener(onChange)
    onScopeDispose(() => mediaQueryList.removeListener(onChange))
  }

  return readonly(matches)
}

/** 响应式布局信息集合 */
export interface UseResponsiveReturn {
  /** 手机档：< 768px，侧边栏改用抽屉 */
  isMobile: Readonly<Ref<boolean>>
  /** 平板档：768–1023px，侧边栏折叠为图标 */
  isTablet: ComputedRef<boolean>
  /** 桌面档：>= 1024px，侧边栏完整展开 */
  isDesktop: Readonly<Ref<boolean>>
  /** 当前档位名称，便于模板里做条件渲染与调试 */
  tier: ComputedRef<LayoutTier>
}

/**
 * 获取当前响应式档位。
 *
 * 实现要点：只监听两个原始媒体查询（手机 / 桌面），「平板」由二者推导，
 * 从而保证三档**互斥且完备**——任何宽度必然命中且只命中一档，
 * 不会出现「两个都为 true」或「都不为 true」导致布局空旷的缝隙状态。
 *
 * 用法：
 * ```ts
 * const { isMobile, isDesktop } = useResponsive()
 * ```
 */
export function useResponsive(): UseResponsiveReturn {
  // 用 -0.98 覆盖小数像素：devicePixelRatio 非整数时 innerWidth 可能为 767.5，
  // 若写成 max-width: 767px 会让 767.5 落进平板档，与 CSS 的 <768 判定不一致。
  const isMobile = useMediaQuery(`(max-width: ${BREAKPOINT_TABLET - 0.98}px)`)
  const isDesktop = useMediaQuery(`(min-width: ${BREAKPOINT_DESKTOP}px)`)

  const isTablet = computed(() => !isMobile.value && !isDesktop.value)
  const tier = computed<LayoutTier>(() => {
    if (isMobile.value) return 'mobile'
    if (isDesktop.value) return 'desktop'
    return 'tablet'
  })

  return { isMobile, isTablet, isDesktop, tier }
}
