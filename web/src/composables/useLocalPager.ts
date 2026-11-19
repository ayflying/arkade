/**
 * 表格分页规格（前端分页）
 *
 * 为什么统一放在这里：容器、编排、镜像、网络、存储卷五张表的分页行为必须一致
 * （每页多少个、越界怎么办、换服务器是否重置）。若各页面自己写数字，
 * 迟早会出现「容器每页 20、镜像每页 15」这种不一致。
 *
 * ## 为什么是前端切片而不是后端分页
 *
 * 这五类资源的列表接口都是一次性返回全量数组：
 * - 容器列表的轮询、指标采样都以「全量容器」为前提（useContainers / useContainerMetrics），
 *   改成后端分页会让指标轮询只能覆盖当前页；
 * - 编排列表要合并 Hub 已纳管与服务器原目录两部分再做统计（见 utils/stackListModel），
 *   分页必须在合并之后做，否则合并与统计都会失真。
 *
 * 所以分页只影响**渲染哪一页**，不影响任何请求、轮询和统计口径。
 */

/** 每页条数：五张表统一 */
export const TABLE_PAGE_SIZE = 20

/**
 * 总页数：至少 1 页（空列表也算一页，分页器据此隐藏）
 */
export function totalPageCount(itemCount: number, pageSize: number = TABLE_PAGE_SIZE): number {
  if (pageSize <= 0) return 1
  return Math.max(1, Math.ceil(itemCount / pageSize))
}

/**
 * 把全量数组切成当前页的数据
 *
 * 两个必须有注释的边界：
 * 1. `page` 可能超出范围（删掉最后一页仅剩的几条、或数据整体变少时），
 *    这里夹紧到最后一页而不是返回空数组，否则用户会看到「空表格」，
 *    实际只是页码越界了；
 * 2. 空数组时返回空数组，不返回 undefined，调用方不必额外判空。
 *
 * 纯函数不修改入参（slice 返回新数组），便于测试与复用。
 */
export function slicePage<T>(items: readonly T[], page: number, pageSize: number = TABLE_PAGE_SIZE): T[] {
  if (pageSize <= 0) return items.slice()
  const totalPages = totalPageCount(items.length, pageSize)
  const safePage = Math.min(Math.max(Math.trunc(page) || 1, 1), totalPages)
  const start = (safePage - 1) * pageSize
  return items.slice(start, start + pageSize)
}

/**
 * 数据变化后的页码：把当前页夹进合法范围
 *
 * 用途：删除最后一页的最后一条、刷新后列表变短、切换服务器导致全新数据时，
 * 当前页可能已经不存在。夹紧而不是重置到第 1 页，
 * 用户刷新第 3 页时期望还停在第 3 页，只有真的越界才回退。
 */
export function clampPage(page: number, itemCount: number, pageSize: number = TABLE_PAGE_SIZE): number {
  const totalPages = totalPageCount(itemCount, pageSize)
  const safePage = Math.trunc(page) || 1
  return Math.min(Math.max(safePage, 1), totalPages)
}

/**
 * 切换数据集（换服务器）时是否要重置页码
 *
 * 换了一台服务器就是另一批资源，沿用旧页码毫无意义，
 * 还会让用户看到一个「凭空出现第 4 页」的列表。
 */
export function shouldResetPage(previousKey: string, nextKey: string): boolean {
  return previousKey !== nextKey
}
