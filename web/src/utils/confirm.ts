/**
 * 二次确认对话框封装
 *
 * 为什么单独抽出来：启动/停止都必须二次确认（docs/02 §6「操作二次确认」优先级高），
 * 两个操作 + 未来更多危险操作会重复写 dialog 参数，
 * 这里统一成 Promise 形式，调用处写 `if (!(await confirmAction(...))) return` 即可。
 *
 * 关于「对话框自动关闭」的坑：
 * Naive UI 的 onPositiveClick 返回 false 可阻止关闭；但我们的回调返回 void，
 * 实测在异步流程里对话框不会自行销毁，会叠在界面上挡住后续操作。
 * 因此这里持有 dialogIns 引用，并在每个回调里显式 destroy()，确保对话框必然关闭。
 *
 * 注意：拿不到 dialog API（桥接尚未挂载）时直接返回 false，
 * 即「默认不执行危险操作」，宁可让用户重试也不要越过确认。
 */

import { getDialog } from './feedback'

export interface ConfirmOptions {
  /** 标题 */
  title: string
  /** 正文说明：要说清「对什么对象做什么」 */
  content: string
  /** 确认按钮文案，默认「确定」 */
  positiveText?: string
  /** 取消按钮文案 */
  negativeText?: string
  /** 是否为危险操作（红色确认按钮） */
  danger?: boolean
}

/** 弹出确认框，用户点确认 resolve true，取消/关闭 resolve false */
export function confirmAction(options: ConfirmOptions): Promise<boolean> {
  const dialog = getDialog()
  if (!dialog) return Promise.resolve(false)

  return new Promise<boolean>((resolve) => {
    let settled = false
    /** 对话框实例引用，用于在回调中显式销毁 */
    let dialogIns: ReturnType<typeof dialog.info> | null = null

    const finish = (value: boolean): void => {
      // 防止 onPositiveClick/onClose 等多条路径重复触发导致重复 resolve
      if (settled) return
      settled = true
      // 先关框再 resolve：保证调用方恢复执行时界面已回到干净状态
      dialogIns?.destroy()
      resolve(value)
    }

    dialogIns = dialog[options.danger === true ? 'warning' : 'info']({
      title: options.title,
      content: options.content,
      positiveText: options.positiveText ?? '确定',
      negativeText: options.negativeText ?? '取消',
      onPositiveClick: (e: MouseEvent) => {
        // 阻止默认关闭行为，统一由 finish() 负责销毁，避免「关两次」的竞态
        e?.preventDefault?.()
        finish(true)
      },
      onNegativeClick: (e: MouseEvent) => {
        e?.preventDefault?.()
        finish(false)
      },
      // 点遮罩/按 ESC/右上角关闭：一律视为取消（不执行危险操作）
      onMaskClick: () => finish(false),
      onClose: () => finish(false)
    })
  })
}
