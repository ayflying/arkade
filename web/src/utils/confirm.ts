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

import { h, ref } from 'vue'
import { NCheckbox } from 'naive-ui'
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

export interface ConfirmWithOptionOptions {
  /** 标题 */
  title: string
  /** 正文说明 */
  content: string
  /** 勾选项的文案 */
  optionLabel: string
  /** 勾选项是否默认勾上；危险操作应保持 false */
  optionDefault?: boolean
  /**
   * 勾选后追加展示的警示文案。
   *
   * 为什么勾选后才显示：不勾选时它是无关信息，会稀释主要说明；
   * 一旦用户主动选择动危险选项，警示必须立刻出现在他眼前。
   */
  optionHint?: string
  positiveText?: string
  negativeText?: string
  danger?: boolean
}

/** 带勾选项的确认结果 */
export interface ConfirmWithOptionResult {
  /** 用户是否点了确认 */
  confirmed: boolean
  /** 勾选项的最终状态（未确认时为 false） */
  optionChecked: boolean
}

/**
 * 弹出带一个勾选项的确认框。
 *
 * 用于「删除编排」这类场景：主操作需要确认，但其中还有一个**默认关闭**的
 * 附加危险操作（是否连带删除数据卷），必须由用户主动勾选。
 *
 * 这样设计的理由：用户点「删除」的意图通常是「删掉这个编排」，
 * 不一定想连数据一起清掉。默认删卷太激进，且不可恢复；
 * 业界惯例（docker compose down 不加 -v 也不删卷）同样是保留。
 */
export function confirmWithOption(
  options: ConfirmWithOptionOptions
): Promise<ConfirmWithOptionResult> {
  const dialog = getDialog()
  // 拿不到 dialog：按「未确认」处理，绝不越过确认执行危险操作
  if (!dialog) return Promise.resolve({ confirmed: false, optionChecked: false })

  return new Promise<ConfirmWithOptionResult>((resolve) => {
    let settled = false
    let dialogIns: ReturnType<typeof dialog.info> | null = null

    /*
     * 勾选状态用 ref 保存，而不是普通变量。
     * 原因：复选框与「勾选后才出现的警示」都要跟着状态变化重新渲染，
     * 普通变量不会触发 Vue 更新，用户勾了之后界面毫无反应。
     * 用 ref 后，content 是一个函数式 VNode，读取 checked.value 时会自动追踪依赖。
     */
    const checked = ref(options.optionDefault === true)

    const finish = (confirmed: boolean): void => {
      if (settled) return
      settled = true
      dialogIns?.destroy()
      resolve({ confirmed, optionChecked: confirmed && checked.value })
    }

    /**
     * 组装正文：说明文字 + 复选框 +（勾选后）警示。
     *
     * 用 render 函数而不是模板，因为 Naive UI 的 content 接受 VNode。
     * 这里返回的是渲染函数，每次 checked 变化都会重新执行，
     * 于是警示文案能随勾选状态实时出现/消失。
     */
    const renderContent = (): ReturnType<typeof h> =>
      h('div', { class: 'confirm-with-option' }, [
        h('p', { class: 'confirm-with-option__text' }, options.content),
        h(
          NCheckbox,
          {
            checked: checked.value,
            onUpdateChecked: (value: boolean) => {
              checked.value = value
            }
          },
          { default: () => options.optionLabel }
        ),
        // 只有勾选后才显示警示：未勾选时它属于噪声
        checked.value && options.optionHint !== undefined
          ? h('p', { class: 'confirm-with-option__hint' }, options.optionHint)
          : null
      ])

    dialogIns = dialog[options.danger === true ? 'warning' : 'info']({
      title: options.title,
      // 传函数：让对话框内容在依赖变化时重新求值
      content: renderContent,
      positiveText: options.positiveText ?? '确定',
      negativeText: options.negativeText ?? '取消',
      onPositiveClick: (e: MouseEvent) => {
        e?.preventDefault?.()
        finish(true)
      },
      onNegativeClick: (e: MouseEvent) => {
        e?.preventDefault?.()
        finish(false)
      },
      onMaskClick: () => finish(false),
      onClose: () => finish(false)
    })
  })
}
