/**
 * 拉镜像输出的纯函数：合并、去重、裁剪、脱敏
 *
 * 从 composable 里抽出来，因为它们是**纯计算**——不碰 timer、不碰响应式，
 * 可以脱离 Vue 直接测。composable 只保留时序逻辑（轮询、代数守卫、状态机）。
 */

/** 一个渲染单元：一次 stdout/stderr 段落 */
export interface OutputChunk {
  seq: number
  stream: 'stdout' | 'stderr' | 'other'
  text: string
}

/** 输入事件的最小形状（避免依赖 types 里的具体类型） */
export interface OutputEvent {
  seq: number
  stream: OutputChunk['stream']
  text: string
}

/**
 * 前端保留上限。
 *
 * 两个维度都要限，因为只用字节数不够：
 * - 空行/极短行文本字节可能为 0，但每条都需要一个 DOM 节点与 seq 记录；
 *   Agent 侧 2000 行输出在前端多轮 poll 累积后，条数可能远超 2000。
 * - 只限条数也不够：单行可能很大（比如一大段 JSON）。
 *
 * 所以字节 + 条数双限。Agent 侧有 2000 行上限，这里给同样的条数上限，
 * 正好兜住"多轮 poll 累积"的溢出。
 */
export const MAX_OUTPUT_BYTES = 256 * 1024
export const MAX_OUTPUT_EVENTS = 2000

/**
 * 计算 UTF-8 字节数。
 *
 * 不能用 `.length`：中文 1 字符 3 字节，emoji 4 字节。
 * 用这个估算会低估 3-4 倍，上限就形同虚设。
 */
export function byteLength(text: string): number {
  return new TextEncoder().encode(text).length
}

/**
 * 把超长文本截到指定字节数以内（保留尾部）。
 *
 * 为什么不直接整条丢弃：一条 300KB 的输出如果被 `break` 掉，
 * 用户会看到**完全空白**的日志——明明命令跑了、有输出，
 * 界面却是空的，这比截断更让人困惑。
 * 保留尾部是合理选择：长输出的最终结论通常在最后。
 *
 * 按 UTF-8 字符边界切，避免切出半个中文变成乱码。
 */
export function truncateTailToBytes(text: string, maxBytes: number): string {
  if (maxBytes <= 0) return ''
  const encoder = new TextEncoder()
  if (encoder.encode(text).length <= maxBytes) return text

  /*
   * 从尾部按 code point 累积，直到超预算。
   * 用 Array.from 而不是索引，能正确处理代理对（emoji）。
   */
  const chars = Array.from(text)
  let used = 0
  let start = chars.length
  for (let i = chars.length - 1; i >= 0; i -= 1) {
    const size = encoder.encode(chars[i]).length
    if (used + size > maxBytes) break
    used += size
    start = i
  }
  return chars.slice(start).join('')
}

/**
 * 合并并裁剪输出。
 *
 * - 按 seq 去重：游标回退会重复给事件，重复渲染让日志出现重复行；
 * - 按 seq 排序：增量到达顺序不保证；
 * - 超出字节或条数上限时**从最旧的开始丢**，用户关心最新输出；
 * - 单条超大时不整条丢弃，而是截尾保留。
 *
 * `dropped` 告知界面"有内容被省略"，不能悄悄丢。
 */
export function mergeOutput(
  existing: readonly OutputChunk[],
  incoming: readonly OutputEvent[],
  maxBytes = MAX_OUTPUT_BYTES,
  maxEvents = MAX_OUTPUT_EVENTS
): { chunks: OutputChunk[]; dropped: boolean } {
  const bySeq = new Map<number, OutputChunk>()
  for (const c of existing) bySeq.set(c.seq, c)
  for (const e of incoming) bySeq.set(e.seq, { seq: e.seq, stream: e.stream, text: e.text })

  const ordered = [...bySeq.values()].sort((a, b) => a.seq - b.seq)
  if (ordered.length === 0) return { chunks: [], dropped: false }

  /*
   * 第一步：先限制条数。超出就从头部丢。
   * 这一步单独做，避免"条数多但每条很小"时被字节预算掩盖。
   */
  let kept = ordered
  let dropped = false
  if (kept.length > maxEvents) {
    kept = kept.slice(kept.length - maxEvents)
    dropped = true
  }

  /*
   * 第二步：把每条都截到不超过总预算，
   * 这样即使某一条特别大，也不会把后面的全挤掉。
   */
  const clamped: OutputChunk[] = kept.map((c) => {
    const size = byteLength(c.text)
    if (size <= maxBytes) return c
    dropped = true
    return { ...c, text: truncateTailToBytes(c.text, maxBytes) }
  })

  /*
   * 第三步：从尾部往前累积到预算内。
   * 单条已保证 ≤ maxBytes，所以至少能留下最后一条——不会出现空日志。
   */
  const result: OutputChunk[] = []
  let used = 0
  for (let i = clamped.length - 1; i >= 0; i -= 1) {
    const c = clamped[i]
    const size = byteLength(c.text)
    if (used + size > maxBytes) {
      dropped = true
      break
    }
    result.push(c)
    used += size
  }
  return { chunks: result.reverse(), dropped }
}

/**
 * 去掉 ANSI 转义序列与终端控制字符。
 *
 * 用正则而不是引入 terminal 包：只有这一处需求，加一个渲染库
 * 换来一整套终端模拟器和更大的包。
 *
 * 同时清掉 `\r`（进度条用回车覆盖，留着会把一行显示成多段）
 * 与其它 C0 控制字符（保留 `\n` `\t`）。
 *
 * ⚠️ 返回纯文本，界面一律用插值渲染，不进 `v-html`。
 */
export function stripAnsi(text: string): string {
  return (
    text
      // CSI / OSC 等 ANSI 转义序列
      // eslint-disable-next-line no-control-regex
      .replace(/\u001B\[[0-9;?]*[A-Za-z]|\u001B\][^\u0007]*\u0007|\u001B[@-Z\\-_]/g, '')
      // 其余 C0 控制字符（保留 \n \t）
      // eslint-disable-next-line no-control-regex
      .replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, '')
      .replace(/\r\n?/g, '\n')
  )
}
