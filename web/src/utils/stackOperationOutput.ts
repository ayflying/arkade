/**
 * 编排操作输出的纯函数：合并、去重、裁剪、脱敏
 *
 * 纯计算，不碰 timer / 响应式，可脱离 Vue 直接测。
 *
 * ## 为什么单独一层
 *
 * 输出处理与「哪个动作」无关：启动、重建、删除的日志处理规则完全一样。
 * 之前只在 pull 里用，现在所有动作共用，所以这里是通用实现。
 *
 * ## 双上限
 *
 * 字节 + 条数都要限：
 * - 只限字节不够：空行文本 0 字节，但每条仍占一个 DOM 节点和 seq 记录；
 * - 只限条数也不够：单行可能很大（一整段 JSON）。
 *
 * Agent 侧有 2000 行上限，这里给同样的条数上限，兜住多轮 poll 累积。
 */

/** 一个渲染单元 */
export interface OutputChunk {
  seq: number
  stream: OutputStream
  text: string
}

/**
 * 输出流。
 *
 * - `command`：每条真实命令执行前的事件（有单一 argv，显示成 `$ 命令`）；
 * - `stage`：**无单一命令的阶段**（如备份时复制文件、打包），只是进度说明文字；
 * - `stdout` / `stderr`：命令的实际输出。
 *
 * `stage` 和 `command` 必须区分：stage 不是一条命令，用 `$` 前缀会误导用户
 * 以为执行了某个程序。两者都不支持百分比——没有可靠的进度来源，
 * 画假进度条只会让人以为卡住了。
 */
export type OutputStream = 'stdout' | 'stderr' | 'command' | 'stage' | 'other'

/** 输入事件的最小形状 */
export interface OutputEvent {
  seq: number
  stream: OutputStream
  text: string
}

export const MAX_OUTPUT_BYTES = 256 * 1024
export const MAX_OUTPUT_EVENTS = 2000

/**
 * 计算 UTF-8 字节数。
 *
 * 不能用 `.length`：中文 1 字符 3 字节，emoji 4 字节。
 * 用这个估算会低估数倍，上限形同虚设。
 */
export function byteLength(text: string): number {
  return new TextEncoder().encode(text).length
}

/**
 * 把超长文本截到指定字节数以内（保留尾部）。
 *
 * 为什么不整条丢弃：一条 300KB 输出若被 `break` 掉，用户会看到**完全空白**的
 * 日志——命令明明跑了、有输出，界面却空的，比截断更困惑。
 * 保留尾部是合理选择：长输出的结论通常在最后。
 *
 * 按 code point 切，避免切出半个中文变乱码。
 */
export function truncateTailToBytes(text: string, maxBytes: number): string {
  if (maxBytes <= 0) return ''
  const encoder = new TextEncoder()
  if (encoder.encode(text).length <= maxBytes) return text

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
 * - 超限时**从最旧的开始丢**，用户关心最新输出；
 * - 单条超大不整条丢弃，而是截尾保留。
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

  let dropped = false
  let kept = ordered
  if (kept.length > maxEvents) {
    kept = kept.slice(kept.length - maxEvents)
    dropped = true
  }

  /*
   * 每条先截到不超总预算：某一条特别大时才不会把后面的全挤掉。
   */
  const clamped: OutputChunk[] = kept.map((c) => {
    if (byteLength(c.text) <= maxBytes) return c
    dropped = true
    return { ...c, text: truncateTailToBytes(c.text, maxBytes) }
  })

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
 * 用正则而非引入 terminal 包：只有这一处需求，加渲染库换来一整套终端模拟器。
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

/**
 * 把输出按「命令分段」组织。
 *
 * Agent 在每条真实命令执行前发一个 `command` 事件。据此把日志切成若干步，
 * 用户能看出多命令是在逐步执行（第 1 步、第 2 步…）。
 *
 * `stage` 事件是**阶段说明**（如备份时复制文件），没有对应命令：
 * 它不开新段，而是归到当前段里、作为一条进度行显示——
 * 否则会出现「第 3 步」却没有命令的空段，反而更混乱。
 */
export interface OutputSegment {
  /** 该段对应的命令；null 表示首个命令之前的前置输出 */
  command: string | null
  /** 该段的输出（不含 command 事件本身） */
  chunks: OutputChunk[]
}

export function segmentOutput(chunks: readonly OutputChunk[]): OutputSegment[] {
  const segments: OutputSegment[] = []
  let current: OutputSegment = { command: null, chunks: [] }

  for (const c of chunks) {
    if (c.stream === 'command') {
      // 前一段有内容才收（避免连续两个 command 产生空段）
      if (current.command !== null || current.chunks.length > 0) segments.push(current)
      current = { command: c.text, chunks: [] }
      continue
    }
    current.chunks.push(c)
  }
  if (current.command !== null || current.chunks.length > 0) segments.push(current)
  return segments
}
