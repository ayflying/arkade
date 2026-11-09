/**
 * 界面偏好设置（本地持久化）
 *
 * 目前只有「自动刷新」一项，但单独开一个 store 而不是塞进 agents/auth：
 * 那些是**服务端数据**的缓存，这个是**本机界面偏好**。混在一起会导致
 * 「刷新一次业务数据把界面设置也重置了」这类问题，且未来再加偏好项（主题、密度）
 * 时不需要改动业务 store。
 *
 * 持久化用 localStorage 而非后端：这是纯本机体验选项，
 * 不同设备上想不一样很正常，也没必要为它增加服务端字段与接口。
 */

import { ref, watch } from 'vue'
import { defineStore } from 'pinia'

/** localStorage 键名，带版本前缀便于将来结构变更时识别 */
const STORAGE_KEY = 'arkade.ui.auto-refresh'

/**
 * 读取初始值。
 *
 * 只有明确存了 'false' 才视为关闭，其它情况（没存过、值被改坏、隐私模式抛异常）
 * 一律按默认开启——自动刷新是「默认好用」的功能，
 * 读取失败时退化成关闭会让用户莫名其妙看到状态不更新。
 */
function readInitial(): boolean {
  try {
    return window.localStorage.getItem(STORAGE_KEY) !== 'false'
  } catch {
    return true
  }
}

export const useUiSettingsStore = defineStore('uiSettings', () => {
  /**
   * 全局「自动刷新」开关，默认开启。
   *
   * 关掉后所有 usePolling 都不再发请求——有些用户会嫌页面动，
   * 也可能在弱网环境下主动想省流量。
   */
  const autoRefresh = ref<boolean>(readInitial())

  // 写入 localStorage：失败（隐私模式/配额满）时静默忽略，只影响持久化不影响本次会话
  watch(autoRefresh, (value) => {
    try {
      window.localStorage.setItem(STORAGE_KEY, value ? 'true' : 'false')
    } catch {
      // 存不下就算了，不打断用户
    }
  })

  /** 切换自动刷新 */
  function setAutoRefresh(value: boolean): void {
    autoRefresh.value = value
  }

  return { autoRefresh, setAutoRefresh }
})
