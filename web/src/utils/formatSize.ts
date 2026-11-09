/**
 * 数值格式化工具（大小、数量）
 *
 * 单独成文件的理由：镜像大小、卷大小这类「字节数」在多个页面出现，
 * 各写一份换算逻辑必然出现「有的算 1024、有的算 1000」的口径不一致。
 */

/**
 * 字节数 → 人类可读大小（1024 进制，与 docker images 的输出口径一致）。
 *
 * 边界：
 * - 负数/非数字/非有限值：返回「—」，不显示 NaN；
 * - 0：返回「0 B」；
 * - 只保留一位小数，且整数时不显示 .0（`512 MB` 而不是 `512.0 MB`）。
 */
export function formatBytes(bytes: number | null | undefined): string {
  if (typeof bytes !== 'number' || !Number.isFinite(bytes) || bytes < 0) return '—'
  if (bytes === 0) return '0 B'

  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB']
  let value = bytes
  let unitIndex = 0
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024
    unitIndex += 1
  }

  // B 级别没有小数意义，直接取整
  if (unitIndex === 0) return `${Math.round(value)} B`

  const rounded = Math.round(value * 10) / 10
  // 整数不显示 .0，读起来更干净
  const text = Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1)
  return `${text} ${units[unitIndex]}`
}

/**
 * 把镜像 ID 缩短成 docker 风格的短 ID（前 12 位）。
 *
 * 边界：ID 本身可能已经带 `sha256:` 前缀，先剥掉再截断；
 * 过短的 ID 原样返回，不做补齐。
 */
export function shortImageId(id: string): string {
  const bare = id.startsWith('sha256:') ? id.slice('sha256:'.length) : id
  return bare.length > 12 ? bare.slice(0, 12) : bare
}
