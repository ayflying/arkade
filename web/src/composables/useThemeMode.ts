/**
 * 主题模式（浅色 / 深色 / 跟随系统）
 *
 * ## 为什么用模块级单例而不是 Pinia store
 * 主题要在**组件树之外**生效：App.vue 的 NConfigProvider 需要它，
 * <html> 上的 data-theme 属性也要它，而这两处都在任何页面之外。
 * 用模块级 ref 做单例最简单，注册时机也不受 Pinia 初始化顺序影响。
 *
 * ## 三态而不是两态
 * "跟随系统"是必须保留的默认值：用户手机在晚上自动切深色，
 * 他打开 Arkade 时期望的是一致的行为，而不是被一个写死的浅色糊一脸。
 * 一旦用户手动选过，就把选择持久化，不再跟随系统。
 *
 * ## 深色写在哪
 * 写 `<html data-theme="dark">`：styles/global.css 的变量块按这个属性切换，
 * 同时把 `color-scheme` 一起设好，让原生控件（滚动条、输入框光标、
 * 日期选择器）也进深色，否则会在深色页里窜出亮色系统控件。
 */

import { computed, readonly, ref } from 'vue'

/** 用户可选的模式 */
export type ThemeMode = 'light' | 'dark' | 'auto'

/** 持久化键：与其它本地偏好一样放在 arkade. 前缀下 */
const STORAGE_KEY = 'arkade.theme.mode'

/** 系统深色媒体查询：不支持 matchMedia 的环境（SSR/测试）视为非深色 */
function systemPrefersDark(): boolean {
  if (typeof window === 'undefined' || typeof window.matchMedia !== 'function') return false
  return window.matchMedia('(prefers-color-scheme: dark)').matches
}

/**
 * 读取初始模式。
 *
 * 存储里没有值时返回 'auto'——这是最不容易出错的默认：
 * 用户从没表达过偏好，就跟随他的系统设置。
 * 存储值非法时同样退回 auto，而不是抛错让页面白屏。
 */
function readStoredMode(): ThemeMode {
  if (typeof window === 'undefined') return 'auto'
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY)
    if (raw === 'light' || raw === 'dark' || raw === 'auto') return raw
  } catch {
    // 隐私模式下 localStorage 可能抛异常，退回默认值即可
  }
  return 'auto'
}

/** 当前模式（用户视角的选择，可能是 auto） */
const mode = ref<ThemeMode>(readStoredMode())

/**
 * 系统是否为深色。独立成一个 ref 是为了让「跟随系统」在系统切换时实时响应：
 * 用户在手机上把系统切到深色，页面应当立刻跟着变，不需要刷新。
 */
const systemDark = ref(systemPrefersDark())

/** 实际生效的是否深色（auto 时看系统） */
const isDark = computed(() => (mode.value === 'auto' ? systemDark.value : mode.value === 'dark'))

/**
 * 把当前状态写进 DOM。
 *
 * 注意 auto 模式下**不写** data-theme，而是交给 CSS 的
 * `prefers-color-scheme` 兜底？——不，这里仍然显式写：
 * 变量块只认 data-theme，显式写 light/dark 能保证 JS 判断与 CSS 变量永远一致，
 * 不会出现「脚本认为深色、CSS 还是浅色」的两套状态。
 */
function applyToDom(): void {
  if (typeof document === 'undefined') return
  const root = document.documentElement
  root.setAttribute('data-theme', isDark.value ? 'dark' : 'light')
  // color-scheme 让原生控件跟随；与 global.css 里的设置双保险
  root.style.colorScheme = isDark.value ? 'dark' : 'light'
}

/** 首次运行即写入，避免首屏按浅色渲染后闪一下 */
applyToDom()

/** 系统主题变化监听：只在浏览器里注册一次 */
if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
  const query = window.matchMedia('(prefers-color-scheme: dark)')
  const onChange = (event: MediaQueryListEvent): void => {
    systemDark.value = event.matches
    // auto 模式下系统切换要立刻生效；手动选择时 isDark 不受影响，写一次也无害
    if (mode.value === 'auto') applyToDom()
  }
  if (typeof query.addEventListener === 'function') query.addEventListener('change', onChange)
  else query.addListener(onChange)
}

/**
 * 切换模式：写存储 + 更新 DOM。
 *
 * @param next 目标模式；传 undefined 表示在浅色/深色之间来回切（跳过 auto）
 */
export function setThemeMode(next: ThemeMode): void {
  mode.value = next
  try {
    window.localStorage.setItem(STORAGE_KEY, next)
  } catch {
    // 存储失败不影响本次生效，只是刷新后回到默认
  }
  applyToDom()
}

/**
 * 供组件使用：当前模式 + 是否深色 + 设置方法。
 *
 * 只读暴露 mode/systemDark，唯一写入口是 setThemeMode，
 * 避免组件直接改 ref 导致 DOM 与存储不同步。
 */
export function useThemeMode(): {
  mode: Readonly<typeof mode>
  isDark: typeof isDark
  setMode: typeof setThemeMode
} {
  return { mode: readonly(mode), isDark, setMode: setThemeMode }
}
