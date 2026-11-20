/**
 * 日志面板的「自动滚动到底部」工具。
 *
 * 容器详情面板和日志抽屉都要做同一件事：内容更新后把可视区滚到最新一行。
 * 两处各写一遍容易出现阈值不一致、行为漂移，这里集中一份。
 */

/**
 * 判定「已在底部」的容差（像素）。
 *
 * 不能要求 scrollTop 严格等于 scrollHeight：不同浏览器滚动位置有舍入误差，
 * 且行高不是整数时永远差零点几像素，严格判断会让跟随永久失效。
 */
export const LOG_BOTTOM_THRESHOLD_PX = 24

/** 滚到最底部。用原生 scrollTop，避免平滑滚动在频繁刷新时互相打断。 */
export function scrollLogToBottom(el: HTMLElement | null): void {
  if (el === null) return
  el.scrollTop = el.scrollHeight
}

/** 当前是否已经在底部附近 */
export function isLogNearBottom(el: HTMLElement | null): boolean {
  if (el === null) return true
  return el.scrollHeight - el.scrollTop - el.clientHeight <= LOG_BOTTOM_THRESHOLD_PX
}
