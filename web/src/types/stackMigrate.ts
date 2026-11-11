/**
 * 迁移目标的服务器选项
 *
 * 只包含渲染下拉真正需要的信息，不直接复用完整 `Agent`：
 * 弹窗只要「能不能选」和「显示什么名字」，多传字段只会让组件
 * 悄悄依赖别处不保证的字段。
 */
export interface StackMigrateTarget {
  id: string
  name: string
  online: boolean
}