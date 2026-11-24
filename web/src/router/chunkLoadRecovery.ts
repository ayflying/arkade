/**
 * 懒加载路由「静默失败」兜底。
 *
 * 背景：每个页面按路由单独拆包。站点升级后，仍旧开着的旧页面里保存的是上一版
 * 的 chunk 文件名，那些文件已经被新镜像替换掉了。此时点击菜单，vue-router 只会
 * 中止这次导航、把错误写进浏览器控制台，界面上什么都不发生——用户看到的就是
 * 「点了没反应」，而且没有任何提示可以解释。
 *
 * 处理：认出「chunk 加载失败」后，带上目标地址整页刷新一次。刷新后加载的是新的
 * 入口文件，chunk 名与本次构建一致，用户会直接落在刚才点的那个页面。
 * 若同一目标在 {@link RELOAD_GUARD_MS} 内已经自动刷新过，说明不是旧缓存问题
 * （例如后端真的挂了），此时不再刷新，只提示手动刷新，避免刷新死循环。
 *
 * 边界：只认下面这些明确的浏览器报错文案，业务异常不会被当成资源问题而整页刷新。
 */
import type { Router } from 'vue-router'
import { toastError } from '@/utils/feedback'

/** 同一目标在此时长内只自动刷新一次，避免刷新死循环 */
export const RELOAD_GUARD_MS = 30_000

/** sessionStorage 键：记录最近一次自动刷新的目标与时间戳 */
export const RELOAD_GUARD_KEY = 'arkade:chunk-reload'

/**
 * 各浏览器对「动态 import 失败」的报错文案并不统一，这里逐个匹配。
 * 只认这些文案，其它错误一律不碰。
 */
const CHUNK_ERROR_PATTERNS = [
  'failed to fetch dynamically imported module', // Chrome / Edge
  'error loading dynamically imported module', // Firefox
  'importing a module script failed', // Safari
  'unable to preload css' // Vite 预加载样式失败
]

/** 把任意抛出物转成可匹配的小写文本 */
function errorText(error: unknown): string {
  if (typeof error === 'string') return error.toLowerCase()
  if (error instanceof Error) return error.message.toLowerCase()
  if (error && typeof error === 'object' && 'message' in error) {
    const message = (error as { message?: unknown }).message
    if (typeof message === 'string') return message.toLowerCase()
  }
  return String(error).toLowerCase()
}

/** 是否是「懒加载 chunk 拿不到」这一类错误 */
export function isChunkLoadError(error: unknown): boolean {
  const text = errorText(error)
  return CHUNK_ERROR_PATTERNS.some((pattern) => text.includes(pattern))
}

/** 只用到读写的存储抽象，便于测试替换 */
export type GuardStorage = Pick<Storage, 'getItem' | 'setItem'>

/**
 * 取 sessionStorage。
 * 隐私模式等场景下访问会抛错，此时退化成「没有记录」——宁可多刷一次也不报错。
 */
function sessionGuard(): GuardStorage | null {
  try {
    return window.sessionStorage
  } catch {
    return null
  }
}

/**
 * 判断这次失败是否值得自动刷新，并把「已刷新」记录下来。
 *
 * @returns 允许刷新返回 true；同一目标刚刷过则返回 false。
 */
export function beginReload(
  target: string,
  stamp: number,
  storage: GuardStorage | null = sessionGuard()
): boolean {
  if (storage) {
    try {
      const raw = storage.getItem(RELOAD_GUARD_KEY)
      if (raw) {
        const previous = JSON.parse(raw) as { target?: unknown; at?: unknown }
        if (
          previous.target === target &&
          typeof previous.at === 'number' &&
          stamp >= previous.at &&
          stamp - previous.at < RELOAD_GUARD_MS
        ) {
          return false
        }
      }
    } catch {
      // 记录损坏就当作没有记录，照常刷新
    }
    try {
      storage.setItem(RELOAD_GUARD_KEY, JSON.stringify({ target, at: stamp }))
    } catch {
      // 写不进去不影响刷新本身
    }
  }
  return true
}

/** 整页刷新实现，抽成参数便于测试 */
export type ReloadImpl = (url: string) => void

export interface ChunkRecoveryOptions {
  /** 覆盖整页刷新行为，默认 window.location.assign */
  reload?: ReloadImpl
  /** 覆盖时间来源，默认 Date.now */
  now?: () => number
  /** 覆盖防重存储；传 null 表示不记录（每次都刷新） */
  storage?: GuardStorage | null
  /** 覆盖提示出口，默认全局 toast */
  notify?: (text: string) => void
}

/**
 * 安装兜底，在 router 创建后调用一次。返回卸载函数。
 */
export function installChunkLoadRecovery(
  router: Router,
  options: ChunkRecoveryOptions = {}
): () => void {
  const reload: ReloadImpl =
    options.reload ?? ((url: string) => window.location.assign(url))
  const now = options.now ?? (() => Date.now())
  const notify = options.notify ?? toastError

  return router.onError((error, to) => {
    if (!isChunkLoadError(error)) return
    const target = to?.fullPath ?? `${window.location.pathname}${window.location.search}`
    const storage = options.storage !== undefined ? options.storage : sessionGuard()
    if (!beginReload(target, now(), storage)) {
      notify('页面资源加载失败，请手动刷新页面后重试')
      return
    }
    notify('检测到站点已更新，正在重新加载页面…')
    reload(target)
  })
}
