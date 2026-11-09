/**
 * 时间格式化工具
 *
 * 后端所有时间字段（last_seen / created）都是 **Unix 秒级** 时间戳，
 * 这里统一提供转换，避免组件里各写一份 *1000 导致口径不一。
 */

/** 秒级时间戳 → 毫秒时间戳；非法值返回 null，由调用方决定展示什么 */
export function secondsToMs(seconds: number | null | undefined): number | null {
  if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds <= 0) return null
  return seconds * 1000
}

/**
 * 相对时间：把时间戳格式化成「刚刚 / 3 分钟前 / 2 小时前 / 5 天前」。
 *
 * 边界处理：
 * - 时间戳为 0 或非法（后端没有心跳过）：返回「—」，而不是显示 1970 年；
 * - 时间戳在未来（服务器与浏览器时钟偏差）：按「刚刚」处理，不显示「-3 分钟前」这种怪值；
 * - 超过 30 天：直接显示「N 天前」，由 absoluteDateTime 提供精确时间兜底。
 */
export function formatRelativeTime(
  seconds: number | null | undefined,
  now: number = Date.now()
): string {
  const ms = secondsToMs(seconds)
  if (ms === null) return '—'

  const diff = now - ms
  if (diff < 0) return '刚刚'

  const minute = 60 * 1000
  const hour = 60 * minute
  const day = 24 * hour

  if (diff < minute) return '刚刚'
  if (diff < hour) return `${Math.floor(diff / minute)} 分钟前`
  if (diff < day) return `${Math.floor(diff / hour)} 小时前`
  return `${Math.floor(diff / day)} 天前`
}

/**
 * 绝对时间：YYYY-MM-DD HH:mm:ss（本地时区）。
 * 用于 tooltip 展示精确时间，与相对时间互补。
 */
export function formatAbsoluteTime(seconds: number | null | undefined): string {
  const ms = secondsToMs(seconds)
  if (ms === null) return '—'
  const d = new Date(ms)
  const pad = (n: number): string => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ` +
    `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  )
}
