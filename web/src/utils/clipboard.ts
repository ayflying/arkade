/**
 * 剪贴板写入：优先 async Clipboard API，非安全上下文退化到 execCommand。
 *
 * ## 为什么不能「不检查返回值就提示已复制」
 *
 * 内网 http 是非安全上下文（window.isSecureContext === false），
 * navigator.clipboard 根本不存在，只能走 document.execCommand('copy')。
 * 实测它在浏览器拒绝时**返回 false 而不抛异常**：页面不可见、失去焦点、
 * 选区不在可编辑元素上，都会失败。调用方不看返回值就会给出「已复制」的
 * 正反馈，而用户粘出来的其实是**上一次**复制的内容（例如刚点过「复制密钥」），
 * 于是表现为「复制编排文件，粘出来还是密钥」。
 *
 * ## 为什么临时元素要落在视口内并校验焦点
 *
 * 旧实现只给 `position: fixed` 而不给 top/left，元素按 body 末尾的静态位置
 * 渲染，长页面里会落到视口外。一旦浏览器拒绝聚焦它，execCommand 复制的就是
 * 页面上**残留的选区**——密钥输入框被点过时 `@focus` 会选中整段密钥。
 * 这里显式把元素固定在视口内，并确认选区确实落在它身上。
 *
 * ## 返回值语义
 *
 * true  = 已经写进剪贴板；
 * false = 浏览器拒绝，调用方必须给出「手动选中后 Ctrl+C」的出口。
 */

/** 临时元素是否可聚焦：聚焦失败说明浏览器拒绝让隐藏元素接管选区 */
function copyWithExecCommand(text: string): boolean {
  const previousFocus = document.activeElement as HTMLElement | null

  // 先清掉页面已有的选区：万一下面的聚焦失败，
  // execCommand 复制到的就是这份旧内容（密钥）而不是新文本。
  window.getSelection()?.removeAllRanges()

  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('aria-hidden', 'true')
  area.setAttribute('tabindex', '-1')
  // fixed 必须带 top/left：只写 position 时元素按 body 末尾的静态位置渲染，
  // 长页面里直接落到视口外。
  area.style.cssText =
    'position:fixed;top:0;left:0;width:1px;height:1px;margin:0;padding:0;border:0;' +
    'outline:none;box-shadow:none;background:transparent;opacity:0.01;z-index:-1;'

  document.body.appendChild(area)

  let copied = false
  try {
    area.focus({ preventScroll: true })
    area.select()
    area.setSelectionRange(0, text.length)
    // 选区必须确实落在临时元素上，否则 execCommand 会复制页面残留选区
    if (document.activeElement === area) {
      copied = document.execCommand('copy')
    }
  } catch {
    copied = false
  } finally {
    area.remove()
    window.getSelection()?.removeAllRanges()
    // 焦点还回去，避免打乱弹窗内的焦点顺序
    previousFocus?.focus?.({ preventScroll: true })
  }

  return copied
}

/** 复制文本；返回是否真的写进了剪贴板 */
export async function copyToClipboard(text: string): Promise<boolean> {
  if (text === '') return false

  if (window.isSecureContext && typeof navigator.clipboard?.writeText === 'function') {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // 权限被拒或文档不在焦点时继续尝试 execCommand
    }
  }

  return copyWithExecCommand(text)
}

/**
 * 选中元素内的文本，供用户手动 Ctrl+C。
 *
 * 复制失败后的兜底：只弹「请手动复制」时，用户往往直接 Ctrl+V 粘贴，
 * 粘出来的还是上一次的剪贴板内容。
 */
export function selectElementText(el: HTMLElement): boolean {
  // 输入框走自己的选区 API，select() 对 readonly 输入框同样有效
  if (el instanceof HTMLInputElement || el instanceof HTMLTextAreaElement) {
    el.focus({ preventScroll: true })
    el.select()
    return true
  }

  const range = document.createRange()
  range.selectNodeContents(el)
  const selection = window.getSelection()
  if (!selection) return false
  selection.removeAllRanges()
  selection.addRange(range)
  return true
}
